package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestFreshlyRegisteredHostIsServedDespiteNegativeCache(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	now := time.Now()
	org := &store.Org{ID: store.NewID("org"), Name: "t", CreatedAt: now}
	if err := data.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	ag := &store.Agent{ID: store.NewID("ag"), OrgID: org.ID, Name: "a", Status: store.AgentActive, CreatedAt: now}
	if err := data.CreateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	rawToken, hash, err := store.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := data.CreateToken(ctx, &store.Token{ID: store.NewID("tok"), OrgID: org.ID, AgentID: ag.ID, Hash: hash, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) }))
	t.Cleanup(origin.Close)

	gw := gateway.New(gateway.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http"})
	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)
	ing := ingress.New(ingress.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost", LookupCacheTTL: time.Minute})
	gw.SetEndpointInvalidator(ing.InvalidateCaches)
	ingSrv := httptest.NewServer(ing)
	t.Cleanup(ingSrv.Close)

	probe := func() int {
		req, _ := http.NewRequest(http.MethodGet, ingSrv.URL+"/", nil)
		req.Host = "late.localhost"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := probe(); code != http.StatusNotFound {
		t.Fatalf("unregistered host status = %d, want 404", code)
	}

	cli := agent.New(agent.Options{
		GatewayURL: "ws" + strings.TrimPrefix(apiSrv.URL, "http"),
		Token:      rawToken,
		Log:        log,
		Endpoints: []agent.EndpointSpec{{
			Kind:        store.KindHTTP,
			Lifecycle:   store.LifecycleEphemeral,
			Subdomain:   "late",
			LocalTarget: mustHost(t, origin.URL),
		}},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Run(runCtx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ep, err := data.GetEndpointBySubdomain(ctx, "late"); err == nil {
			if _, ok := conns.ResolveEndpoint(ep.ID); ok {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code := probe(); code != http.StatusOK {
		t.Fatalf("just-registered host status = %d, want 200 (stale negative cache)", code)
	}
}
