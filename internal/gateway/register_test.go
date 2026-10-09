package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type fakePorts struct {
	err  error
	next int
}

func (f *fakePorts) Open(_ string, requested int) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if requested > 0 {
		return requested, nil
	}
	f.next++
	return 30000 + f.next, nil
}

func (f *fakePorts) Close(string) {}

func newRegisterFixture(t *testing.T, opts Options) (*Gateway, *store.Agent, *store.Agent, store.DataStore) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	now := time.Now()
	org := &store.Org{ID: "org1", Name: "o", CreatedAt: now}
	if err := data.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	mk := func(id string) *store.Agent {
		a := &store.Agent{ID: id, OrgID: org.ID, Name: id, Status: store.AgentActive, CreatedAt: now}
		if err := data.CreateAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	opts.Data = data
	opts.Conns = memory.NewConnStore()
	opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	opts.BaseDomain = "example.com"
	opts.PublicScheme = "https"
	return New(opts), mk("ag1"), mk("ag2"), data
}

func TestRegisterReasons(t *testing.T) {
	tests := []struct {
		name      string
		opts      Options
		seed      []tunnel.EndpointRequest
		quota     int
		req       tunnel.EndpointRequest
		wantError string
		wantURL   string
	}{
		{
			name:    "http with subdomain",
			req:     tunnel.EndpointRequest{Ref: "0", Kind: store.KindHTTP, Subdomain: "shop"},
			wantURL: "https://shop.example.com",
		},
		{
			name:      "subdomain taken by another agent",
			seed:      []tunnel.EndpointRequest{{Ref: "s", Kind: store.KindHTTP, Subdomain: "shop"}},
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindHTTP, Subdomain: "shop"},
			wantError: `subdomain "shop" is already taken`,
		},
		{
			name:      "reserved subdomain",
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindHTTP, Subdomain: "admin"},
			wantError: `subdomain "admin" is reserved`,
		},
		{
			name:      "invalid subdomain",
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindHTTP, Subdomain: "-bad_"},
			wantError: "invalid subdomain",
		},
		{
			name:      "quota exceeded",
			quota:     1,
			seed:      []tunnel.EndpointRequest{{Ref: "s", Kind: store.KindHTTP, Subdomain: "first"}},
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindHTTP, Subdomain: "second"},
			wantError: "quota exceeded",
		},
		{
			name:      "tcp disabled",
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindTCP},
			wantError: "tcp ingress is disabled",
		},
		{
			name:      "tcp disabled with configured reason",
			opts:      Options{KindUnavailable: map[string]string{store.KindTCP: "tcp ingress is off (set MISHMESH_TCP_ENABLED=true)"}},
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindTCP},
			wantError: "MISHMESH_TCP_ENABLED",
		},
		{
			name:      "tcp port in use",
			opts:      Options{Ports: &fakePorts{err: errors.New("port 10005 is already in use")}},
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindTCP, Port: 10005},
			wantError: "tcp port 10005 unavailable: port 10005 is already in use",
		},
		{
			name:    "tcp requested port",
			opts:    Options{Ports: &fakePorts{}},
			req:     tunnel.EndpointRequest{Ref: "0", Kind: store.KindTCP, Port: 10005},
			wantURL: "tcp://example.com:10005",
		},
		{
			name:      "http ingress disabled",
			opts:      Options{KindUnavailable: map[string]string{store.KindHTTP: "http ingress is disabled on this server"}},
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindHTTP},
			wantError: "http ingress is disabled",
		},
		{
			name:    "tls with subdomain",
			req:     tunnel.EndpointRequest{Ref: "0", Kind: store.KindTLS, Subdomain: "secure"},
			wantURL: "tls://secure.example.com:8444",
		},
		{
			name:      "tls custom domain not registered",
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindTLS, Domain: "api.example.org"},
			wantError: "not registered",
		},
		{
			name:      "subdomain and domain together",
			req:       tunnel.EndpointRequest{Ref: "0", Kind: store.KindTLS, Subdomain: "a", Domain: "b.example.org"},
			wantError: "mutually exclusive",
		},
		{
			name:      "unknown kind",
			req:       tunnel.EndpointRequest{Ref: "0", Kind: "udp"},
			wantError: `unsupported endpoint kind "udp"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, ag1, ag2, data := newRegisterFixture(t, tt.opts)
			ctx := context.Background()
			if tt.quota > 0 {
				if err := data.SetQuota(ctx, &store.Quota{OrgID: "org1", MaxEndpoints: tt.quota}); err != nil {
					t.Fatal(err)
				}
			}
			if len(tt.seed) > 0 {
				ack := g.handleRegister(ctx, ag2, &tunnel.RegisterPayload{Endpoints: tt.seed})
				if ack.Endpoints[0].Error != "" {
					t.Fatalf("seed failed: %s", ack.Endpoints[0].Error)
				}
			}
			ack := g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{tt.req}})
			if len(ack.Endpoints) != 1 {
				t.Fatalf("got %d bindings", len(ack.Endpoints))
			}
			b := ack.Endpoints[0]
			if b.Ref != tt.req.Ref {
				t.Fatalf("ref = %q, want %q", b.Ref, tt.req.Ref)
			}
			if tt.wantError != "" {
				if !strings.Contains(b.Error, tt.wantError) {
					t.Fatalf("error = %q, want containing %q", b.Error, tt.wantError)
				}
				if b.EndpointID != "" {
					t.Fatalf("failed binding carries endpoint id %q", b.EndpointID)
				}
				return
			}
			if b.Error != "" {
				t.Fatalf("unexpected error: %s", b.Error)
			}
			if b.EndpointID == "" || b.PublicURL != tt.wantURL {
				t.Fatalf("binding = %+v, want url %q", b, tt.wantURL)
			}
		})
	}
}

func TestRegisterHTTPIncludesPathURL(t *testing.T) {
	g, ag1, _, _ := newRegisterFixture(t, Options{})
	ack := g.handleRegister(context.Background(), ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "0", Kind: store.KindHTTP, Subdomain: "shop"}}})
	b := ack.Endpoints[0]
	if want := "https://example.com/tunnel/" + b.EndpointID; b.PathURL != want {
		t.Fatalf("path url = %q, want %q", b.PathURL, want)
	}
}

func TestRegisterHTTPWithPathRoutingDisabled(t *testing.T) {
	g, agent, _, _ := newRegisterFixture(t, Options{DisablePathRouting: true})
	ack := g.handleRegister(context.Background(), agent, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "0", Kind: store.KindHTTP, Subdomain: "shop"}}})
	binding := ack.Endpoints[0]
	if binding.Error != "" || binding.PathURL != "" || binding.PublicURL != "https://shop.example.com" {
		t.Fatalf("unexpected binding: %+v", binding)
	}
}

func TestRegisterTCPRebindsOwnReservedPort(t *testing.T) {
	g, agent, other, data := newRegisterFixture(t, Options{Ports: &fakePorts{}})
	ctx := context.Background()
	request := tunnel.EndpointRequest{Kind: store.KindTCP, Port: 10001, Lifecycle: store.LifecycleReserved}
	first, err := g.registerOne(ctx, agent, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.registerOne(ctx, agent, request)
	if err != nil || second.EndpointID != first.EndpointID || second.PublicURL != "tcp://example.com:10001" {
		t.Fatalf("rebind %+v, first %+v, err %v", second, first, err)
	}
	endpoints, err := data.ListEndpointsByAgent(ctx, agent.ID)
	if err != nil || len(endpoints) != 1 {
		t.Fatalf("endpoints = %d, err = %v", len(endpoints), err)
	}
	g.ports = &fakePorts{err: errors.New("port occupied")}
	if _, err := g.registerOne(ctx, other, request); err == nil {
		t.Fatal("another agent must not reuse the reserved port")
	}
}

func TestRegisterRebindsOwnSubdomainAndCustomDomain(t *testing.T) {
	g, ag1, _, data := newRegisterFixture(t, Options{})
	ctx := context.Background()
	reserved := &store.Endpoint{ID: "ep_dom", AgentID: ag1.ID, OrgID: "org1", Kind: store.KindTLS, Lifecycle: store.LifecycleReserved, Domain: "api.example.org", CreatedAt: time.Now()}
	if err := data.CreateEndpoint(ctx, reserved); err != nil {
		t.Fatal(err)
	}
	reqs := []tunnel.EndpointRequest{
		{Ref: "a", Kind: store.KindHTTP, Subdomain: "shop", Lifecycle: store.LifecycleReserved},
		{Ref: "d", Kind: store.KindTLS, Domain: "api.example.org"},
	}
	first := g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: reqs})
	second := g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: reqs})
	for i := range reqs {
		if first.Endpoints[i].Error != "" || second.Endpoints[i].Error != "" {
			t.Fatalf("rebind failed: %+v / %+v", first.Endpoints[i], second.Endpoints[i])
		}
		if first.Endpoints[i].EndpointID != second.Endpoints[i].EndpointID {
			t.Fatalf("endpoint %d changed across registrations", i)
		}
	}
	if got := second.Endpoints[1]; got.EndpointID != "ep_dom" || got.PublicURL != "tls://api.example.org:8444" {
		t.Fatalf("domain binding = %+v", got)
	}
}
