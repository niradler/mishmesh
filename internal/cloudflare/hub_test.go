package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mishmesh/mishmesh/internal/connections"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type hubFixture struct {
	hub  *connections.Hub
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
	hub := genericCloudflareHub(t, data, box, f)
	return &hubFixture{hub: hub, data: data, box: box, f: f}
}

func genericCloudflareHub(t *testing.T, data *sqlite.Store, box *secrets.Box, fake *fakeCF) *connections.Hub {
	t.Helper()
	registry := connections.NewRegistry()
	if err := registry.Register(NewProvider(ProviderOptions{BaseURL: fake.server.URL})); err != nil {
		t.Fatal(err)
	}
	return connections.NewHub(connections.HubOptions{Registry: registry, Deps: connections.Deps{Data: data, Box: box, Origin: testOrigin}})
}

func (x *hubFixture) save(t *testing.T, apiToken string) {
	t.Helper()
	cfg := providerConfig(t, "conn-a", x.f.local.addr(), apiToken != "")
	cfg.Record.OrgID = "org_a"
	raw, err := json.Marshal(cfg.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Record.SecretsEnc, err = x.box.Seal(raw, connections.SecretContext("org_a", "conn-a"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Record.CreatedAt = time.Now()
	cfg.Record.UpdatedAt = time.Now()
	if old, err := x.data.GetConnection(context.Background(), "conn-a"); err == nil {
		cfg.Record.CreatedAt = old.CreatedAt
		if err := x.data.UpdateConnection(context.Background(), &cfg.Record); err != nil {
			t.Fatal(err)
		}
	} else if errors.Is(err, store.ErrNotFound) {
		if err := x.data.CreateConnection(context.Background(), &cfg.Record); err != nil {
			t.Fatal(err)
		}
	} else {
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
	waitFor(t, func() bool { return !x.hub.Status("conn-a").LastSync.IsZero() })
	if st := x.hub.Status("conn-a"); st.Level != LevelImported || st.Details["origin"] != testOrigin {
		t.Fatalf("status = %+v", st)
	}
	if st, err := x.hub.Sync(ctx, "conn-a"); err != nil || st.Level != LevelImported {
		t.Fatalf("imported refresh = %+v, %v", st, err)
	}

	x.save(t, testToken)
	if err := x.hub.Reload(ctx, "conn-a"); err != nil {
		t.Fatal(err)
	}
	st, err := x.hub.Sync(ctx, "conn-a")
	if err != nil || st.Level != LevelManaged || tunnelGets(x.f) == 0 {
		t.Fatalf("managed sync = %+v, %v", st, err)
	}

	if err := x.data.DeleteConnection(ctx, "conn-a"); err != nil {
		t.Fatal(err)
	}
	if err := x.hub.Reload(ctx, "conn-a"); err != nil {
		t.Fatal(err)
	}
	if st := x.hub.Status("conn-a"); st.Health != "down" {
		t.Fatalf("status after delete = %+v", st)
	}
	if _, err := x.hub.Sync(ctx, "conn-a"); !errors.Is(err, connections.ErrNotConnected) {
		t.Fatalf("sync without connection = %v", err)
	}
}

func TestHubReportsUndecryptableConnection(t *testing.T) {
	x := newHubFixture(t)
	ctx := context.Background()
	x.save(t, "")
	other, _ := secrets.New(append(make([]byte, 31), 1))
	x.hub = genericCloudflareHub(t, x.data, other, x.f)
	if err := x.hub.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer x.hub.Close()
	st := x.hub.Status("conn-a")
	if st.Health != "down" || st.LastError == "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestHubReloadBeforeStart(t *testing.T) {
	x := newHubFixture(t)
	if err := x.hub.Reload(context.Background(), "org_a"); !errors.Is(err, connections.ErrHubStopped) {
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
