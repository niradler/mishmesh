package e2e

import (
	"context"
	"encoding/json"
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
	"github.com/mishmesh/mishmesh/internal/metrics"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type stackOptions struct {
	subdomain      string
	localTarget    string
	maxBandwidth   int64
	policy         *store.EndpointPolicy
	ingressOptions func(*ingress.Options)
}

type stack struct {
	data     store.DataStore
	conns    *memory.ConnStore
	metrics  *metrics.Metrics
	ingress  *httptest.Server
	endpoint *store.Endpoint
	orgID    string
}

func startStack(t testing.TB, backend http.Handler, opts stackOptions) *stack {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	if opts.subdomain == "" {
		opts.subdomain = "demo"
	}
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "stack.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
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
	if opts.maxBandwidth > 0 {
		if err := data.SetQuota(ctx, &store.Quota{OrgID: org.ID, MaxBandwidthBytes: opts.maxBandwidth, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	target := opts.localTarget
	if target == "" {
		origin := httptest.NewServer(backend)
		t.Cleanup(origin.Close)
		target = mustHost(t, origin.URL)
	}

	var policy json.RawMessage
	if opts.policy != nil {
		policy, err = json.Marshal(opts.policy)
		if err != nil {
			t.Fatal(err)
		}
	}

	mx := metrics.New()
	gw := gateway.New(gateway.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http", Metrics: mx})
	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	ingOpts := ingress.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost", Meter: mx}
	if opts.ingressOptions != nil {
		opts.ingressOptions(&ingOpts)
	}
	ingSrv := httptest.NewServer(ingress.New(ingOpts))
	t.Cleanup(ingSrv.Close)

	cli := agent.New(agent.Options{
		GatewayURL: "ws" + strings.TrimPrefix(apiSrv.URL, "http"),
		Token:      rawToken,
		Log:        log,
		Endpoints: []agent.EndpointSpec{{
			Kind:        store.KindHTTP,
			Lifecycle:   store.LifecycleReserved,
			Subdomain:   opts.subdomain,
			LocalTarget: target,
			Policy:      policy,
		}},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Run(runCtx) }()

	s := &stack{data: data, conns: conns, metrics: mx, ingress: ingSrv, orgID: org.ID}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ep, err := data.GetEndpointBySubdomain(ctx, opts.subdomain); err == nil {
			if _, ok := conns.ResolveEndpoint(ep.ID); ok {
				s.endpoint = ep
				time.Sleep(50 * time.Millisecond)
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tunnel not ready")
	return nil
}

func (s *stack) request(method, host, path string, body io.Reader) *http.Request {
	req, _ := http.NewRequest(method, s.ingress.URL+path, body)
	req.Host = host
	return req
}

func (s *stack) scrape(t testing.TB) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}
