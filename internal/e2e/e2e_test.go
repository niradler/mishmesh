package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestEndToEndHTTPTunnel(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	data, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()

	rawToken, agentID := seed(t, ctx, data)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(origin.Close)

	gw := gateway.New(gateway.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http"})
	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	ing := ingress.New(ingress.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost"})
	ingSrv := httptest.NewServer(ing)
	t.Cleanup(ingSrv.Close)

	cli := agent.New(agent.Options{
		GatewayURL: "ws" + strings.TrimPrefix(apiSrv.URL, "http"),
		Token:      rawToken,
		Log:        log,
		Endpoints: []agent.EndpointSpec{{
			Kind:        store.KindHTTP,
			Lifecycle:   store.LifecycleReserved,
			Subdomain:   "demo",
			LocalTarget: mustHost(t, origin.URL),
		}},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Run(runCtx) }()

	t.Run("subdomain routing", func(t *testing.T) {
		body := pollTunnel(t, ingSrv, "demo.localhost", "/world")
		if body != "hello GET /world" {
			t.Fatalf("got %q", body)
		}
	})

	t.Run("path routing strips prefix", func(t *testing.T) {
		ep, err := data.GetEndpointBySubdomain(ctx, "demo")
		if err != nil {
			t.Fatalf("get endpoint: %v", err)
		}
		body := pollTunnel(t, ingSrv, "", "/tunnel/"+ep.ID+"/foo/bar")
		if body != "hello GET /foo/bar" {
			t.Fatalf("got %q", body)
		}
	})

	_ = agentID
}

func seed(t *testing.T, ctx context.Context, data store.DataStore) (rawToken, agentID string) {
	t.Helper()
	now := time.Now()
	org := &store.Org{ID: store.NewID("org"), Name: "t", CreatedAt: now}
	if err := data.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	ag := &store.Agent{ID: store.NewID("ag"), OrgID: org.ID, Name: "a", Status: store.AgentActive, CreatedAt: now}
	if err := data.CreateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	raw, hash, err := store.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := data.CreateToken(ctx, &store.Token{ID: store.NewID("tok"), OrgID: org.ID, AgentID: ag.ID, Hash: hash, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return raw, ag.ID
}

func pollTunnel(t *testing.T, srv *httptest.Server, host, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := srv.Client().Do(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return string(b)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("tunnel not ready for host=%q path=%q", host, path)
	return ""
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
