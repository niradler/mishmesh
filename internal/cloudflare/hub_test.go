package cloudflare

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type hubFixture struct {
	hub  *Hub
	data *sqlite.Store
	box  *secrets.Box
	f    *fakeCF
}

func newHubFixture(t *testing.T) *hubFixture {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "org_a", Name: "a", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeCF(t)
	hub := NewHub(HubOptions{Data: data, Box: box, Origin: testOrigin, BaseURL: f.server.URL, SyncInterval: time.Hour})
	return &hubFixture{hub: hub, data: data, box: box, f: f}
}

func (x *hubFixture) save(t *testing.T, apiToken string) {
	t.Helper()
	tunnelEnc, err := x.box.Seal([]byte("tunnel-token"), TokenContext("org_a", TokenKindTunnel))
	if err != nil {
		t.Fatal(err)
	}
	conn := &store.CloudflareConnection{OrgID: "org_a", Name: "lab", AccountID: testAccount, TunnelID: testTunnel,
		TunnelTokenEnc: tunnelEnc, Connector: ConnectorExternal, MetricsAddr: x.f.local.addr(), UpdatedAt: time.Now()}
	if apiToken != "" {
		if conn.APITokenEnc, err = x.box.Seal([]byte(apiToken), TokenContext("org_a", TokenKindAPI)); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.data.SetCloudflareConnection(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
}

func TestHubLifecycle(t *testing.T) {
	x := newHubFixture(t)
	ctx := context.Background()
	x.save(t, "")
	if err := x.hub.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer x.hub.Close()
	waitFor(t, func() bool { return !x.hub.Status("org_a").LastSync.IsZero() })
	if st := x.hub.Status("org_a"); st.Level != LevelImported || st.Origin != testOrigin {
		t.Fatalf("status = %+v", st)
	}
	if st, err := x.hub.Sync(ctx, "org_a"); err != nil || st.Level != LevelImported {
		t.Fatalf("imported refresh = %+v, %v", st, err)
	}

	x.save(t, testToken)
	if err := x.hub.Reload(ctx, "org_a"); err != nil {
		t.Fatal(err)
	}
	st, err := x.hub.Sync(ctx, "org_a")
	if err != nil || st.Level != LevelManaged || tunnelGets(x.f) == 0 {
		t.Fatalf("managed sync = %+v, %v", st, err)
	}

	if err := x.data.DeleteCloudflareConnection(ctx, "org_a"); err != nil {
		t.Fatal(err)
	}
	if err := x.hub.Reload(ctx, "org_a"); err != nil {
		t.Fatal(err)
	}
	if st := x.hub.Status("org_a"); st.Level != LevelNone {
		t.Fatalf("status after delete = %+v", st)
	}
	if _, err := x.hub.Sync(ctx, "org_a"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("sync without connection = %v", err)
	}
}

func TestHubReportsUndecryptableConnection(t *testing.T) {
	x := newHubFixture(t)
	ctx := context.Background()
	x.save(t, "")
	other, _ := secrets.New(append(make([]byte, 31), 1))
	x.hub.opts.Box = other
	if err := x.hub.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer x.hub.Close()
	st := x.hub.Status("org_a")
	if st.Level != LevelNone || st.LastError == "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestHubReloadBeforeStart(t *testing.T) {
	x := newHubFixture(t)
	if err := x.hub.Reload(context.Background(), "org_a"); !errors.Is(err, errHubStopped) {
		t.Fatalf("err = %v", err)
	}
}

func TestOriginForListenAddr(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"127.0.0.1:8090", "http://127.0.0.1:8090"},
		{":8080", "http://127.0.0.1:8080"},
		{"0.0.0.0:8080", "http://127.0.0.1:8080"},
		{"[::]:8080", "http://127.0.0.1:8080"},
		{"[::1]:8080", "http://[::1]:8080"},
		{"ingress.internal:80", "http://ingress.internal:80"},
		{"", ""},
		{"no-port", ""},
	}
	for _, tc := range cases {
		if got := OriginForListenAddr(tc.addr); got != tc.want {
			t.Errorf("OriginForListenAddr(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
