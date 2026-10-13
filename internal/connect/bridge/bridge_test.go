package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type echoOrigin struct {
	id    string
	kinds chan string
}

func (o *echoOrigin) AgentID() string { return o.id }
func (o *echoOrigin) Close() error    { return nil }

func (o *echoOrigin) OpenStream(_ context.Context, _, kind string, _ map[string]string) (net.Conn, error) {
	o.kinds <- kind
	near, far := net.Pipe()
	go func() { _, _ = io.Copy(far, far); _ = far.Close() }()
	return near, nil
}

func TestHandleConnect(t *testing.T) {
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	origin := &echoOrigin{id: "ag_origin", kinds: make(chan string, 8)}
	conns.AddAgent(origin)

	org := &store.Org{ID: "org_a", Name: "a", CreatedAt: time.Now()}
	if err := data.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateAgent(ctx, &store.Agent{ID: origin.id, OrgID: org.ID, Name: "office", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	endpoint := func(name, kind string, allow []string, online bool) {
		ep := &store.Endpoint{
			ID: store.NewID("ep"), AgentID: origin.id, OrgID: org.ID, Kind: kind, Method: store.MethodNative,
			Lifecycle: store.LifecycleReserved, CreatedAt: time.Now(),
			Policy: &store.EndpointPolicy{Connect: &store.ConnectPolicy{Name: name, AllowAgents: allow}},
		}
		if err := data.CreateEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
		if online {
			conns.BindEndpoint(ep.ID, origin.id)
		}
	}
	endpoint("vpn", store.KindUDP, []string{"laptop"}, true)
	endpoint("by-id", store.KindUDP, []string{"ag_laptop"}, true)
	endpoint("anyone", store.KindTCP, []string{store.ConnectAnyAgent}, true)
	endpoint("nobody", store.KindTCP, nil, true)
	endpoint("offline", store.KindTCP, []string{store.ConnectAnyAgent}, false)

	laptop := &store.Agent{ID: "ag_laptop", OrgID: org.ID, Name: "laptop"}
	stranger := &store.Agent{ID: "ag_stranger", OrgID: org.ID, Name: "stranger"}
	outsider := &store.Agent{ID: "ag_out", OrgID: "org_b", Name: "laptop"}

	tests := []struct {
		name    string
		agent   *store.Agent
		connect string
		kind    string
		wantErr error
	}{
		{"allowed by name", laptop, "vpn", store.KindUDP, nil},
		{"allowed by id", laptop, "by-id", store.KindUDP, nil},
		{"wildcard", stranger, "anyone", store.KindTCP, nil},
		{"not on list", stranger, "vpn", store.KindUDP, ErrNotAllowed},
		{"empty allow list", laptop, "nobody", store.KindTCP, ErrNotAllowed},
		{"kind mismatch", laptop, "vpn", store.KindTCP, ErrKindMismatch},
		{"origin offline", laptop, "offline", store.KindTCP, ErrOriginOffline},
		{"missing ref", laptop, "", store.KindTCP, ErrMissingConnect},
		{"other org", outsider, "vpn", store.KindUDP, store.ErrNotFound},
	}
	b := New(Options{Data: data, Conns: conns, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			init := tunnel.StreamInit{Kind: tt.kind, Meta: map[string]string{tunnel.MetaConnect: tt.connect}}
			_, _, err := b.authorize(ctx, tt.agent, init)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("authorize err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() { b.HandleConnect(ctx, tt.agent, server, init); close(done) }()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := client.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4)
			if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "ping" {
				t.Fatalf("echo = %q, err %v", buf, err)
			}
			if got := <-origin.kinds; got != tt.kind {
				t.Fatalf("origin opened kind %q, want %q", got, tt.kind)
			}
			_ = client.Close()
			<-done
		})
	}
}
