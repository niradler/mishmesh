package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type multiEnv struct {
	data    store.DataStore
	conns   store.ConnectionStore
	gwURL   string
	ingSrv  *httptest.Server
	token   string
	agentID string
	log     *slog.Logger
}

func newMultiEnv(t *testing.T, gwOpts gateway.Options) *multiEnv {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "multi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	token, agentID := seed(t, context.Background(), data)

	gwOpts.Data, gwOpts.Conns, gwOpts.Log = data, conns, log
	gwOpts.BaseDomain, gwOpts.PublicScheme = "localhost", "http"
	gw := gateway.New(gwOpts)
	mux := http.NewServeMux()
	mux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	apiSrv := httptest.NewServer(mux)
	t.Cleanup(apiSrv.Close)

	ing := ingress.New(ingress.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost"})
	ingSrv := httptest.NewServer(ing)
	t.Cleanup(ingSrv.Close)

	return &multiEnv{data: data, conns: conns, gwURL: apiSrv.URL, ingSrv: ingSrv, token: token, agentID: agentID, log: log}
}

func (e *multiEnv) newAgent(out io.Writer, specs ...agent.EndpointSpec) *agent.Agent {
	return agent.New(agent.Options{GatewayURL: e.gwURL, Token: e.token, Log: e.log, Out: out, Endpoints: specs})
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestMultipleTunnelsOverOneSession(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "multi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	token, agentID := seed(t, ctx, data)

	tcpIng := ingress.NewTCP(ingress.TCPOptions{Conns: conns, Log: log, BindHost: "127.0.0.1", PortMin: 24200, PortMax: 24300})
	t.Cleanup(tcpIng.Shutdown)
	gw := gateway.New(gateway.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http", Ports: tcpIng})
	mux := http.NewServeMux()
	mux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	apiSrv := httptest.NewServer(mux)
	t.Cleanup(apiSrv.Close)
	ing := ingress.New(ingress.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost"})
	ingSrv := httptest.NewServer(ing)
	t.Cleanup(ingSrv.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "web %s", r.URL.Path)
	}))
	t.Cleanup(origin.Close)
	echoAddr := startEchoServer(t)

	out := &syncBuffer{}
	cli := agent.New(agent.Options{
		GatewayURL: "ws" + strings.TrimPrefix(apiSrv.URL, "http"),
		Token:      token,
		Log:        log,
		Out:        out,
		Endpoints: []agent.EndpointSpec{
			{Name: "web", Kind: store.KindHTTP, Lifecycle: store.LifecycleReserved, Subdomain: "multi", LocalTarget: mustHost(t, origin.URL)},
			{Name: "db", Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, LocalTarget: echoAddr},
		},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Run(runCtx) }()

	if body := pollTunnel(t, ingSrv, "multi.localhost", "/hello"); body != "web /hello" {
		t.Fatalf("http tunnel body = %q", body)
	}

	port := pollTCPPort(t, ctx, data, agentID)
	echoOver(t, fmt.Sprintf("127.0.0.1:%d", port))

	eps, err := data.ListEndpointsByAgent(ctx, agentID)
	if err != nil || len(eps) != 2 {
		t.Fatalf("endpoints = %d, %v; want 2 over one session", len(eps), err)
	}

	waitFor(t, "startup report printed", func() bool { return strings.Contains(out.String(), "endpoint:") })
	report := out.String()
	var httpID string
	for _, ep := range eps {
		if ep.Kind == store.KindHTTP {
			httpID = ep.ID
		}
	}
	for _, want := range []string{
		"web",
		"http://multi.localhost",
		"path:     http://localhost/tunnel/" + httpID,
		fmt.Sprintf("tcp://localhost:%d", port),
		"endpoint: " + httpID,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q\n%s", want, report)
		}
	}
}

func TestRegistrationFailureReasonReachesAgent(t *testing.T) {
	env := newMultiEnv(t, gateway.Options{})
	ctx := context.Background()
	now := time.Now()
	otherAgent := &store.Agent{ID: "ag_other", OrgID: "org_other", Name: "other", Status: store.AgentActive, CreatedAt: now}
	if err := env.data.CreateOrg(ctx, &store.Org{ID: "org_other", Name: "other", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := env.data.CreateAgent(ctx, otherAgent); err != nil {
		t.Fatal(err)
	}
	if err := env.data.CreateEndpoint(ctx, &store.Endpoint{ID: "ep_taken", AgentID: otherAgent.ID, OrgID: otherAgent.OrgID, Kind: store.KindHTTP, Lifecycle: store.LifecycleReserved, Subdomain: "taken", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	out := &syncBuffer{}
	cli := env.newAgent(out,
		agent.EndpointSpec{Name: "ok", Kind: store.KindHTTP, LocalTarget: "127.0.0.1:1"},
		agent.EndpointSpec{Name: "dup", Kind: store.KindHTTP, Subdomain: "taken", LocalTarget: "127.0.0.1:1"},
		agent.EndpointSpec{Name: "db", Kind: store.KindTCP, LocalTarget: "127.0.0.1:1"},
	)
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := cli.Run(runCtx)

	var regErr *agent.RegistrationError
	if !errors.As(err, &regErr) {
		t.Fatalf("err = %v, want RegistrationError", err)
	}
	if regErr.Failed != 2 || regErr.Total != 3 {
		t.Fatalf("failed/total = %d/%d", regErr.Failed, regErr.Total)
	}
	report := out.String()
	for _, want := range []string{
		`dup  http  127.0.0.1:1  FAILED: subdomain "taken" is already taken`,
		"db   tcp   127.0.0.1:1  FAILED: tcp ingress is disabled on this server",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q\n%s", want, report)
		}
	}
}

func TestDisabledIngressReasonIsReported(t *testing.T) {
	env := newMultiEnv(t, gateway.Options{KindUnavailable: map[string]string{
		store.KindHTTP: "http ingress is disabled on this server (MISHMESH_INGRESS_ENABLED=false)",
	}})
	out := &syncBuffer{}
	cli := env.newAgent(out, agent.EndpointSpec{Name: "web", Kind: store.KindHTTP, LocalTarget: "127.0.0.1:1"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var regErr *agent.RegistrationError
	if err := cli.Run(ctx); !errors.As(err, &regErr) {
		t.Fatalf("err = %v, want RegistrationError", err)
	}
	if !strings.Contains(out.String(), "FAILED: http ingress is disabled on this server") {
		t.Fatalf("report:\n%s", out.String())
	}
}

func TestAuthFailuresExitWithoutRetry(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, env *multiEnv)
		want   error
	}{
		{
			name: "revoked token",
			mutate: func(t *testing.T, env *multiEnv) {
				if err := env.data.RevokeTokensByAgent(context.Background(), env.agentID); err != nil {
					t.Fatal(err)
				}
			},
			want: agent.ErrTokenRejected,
		},
		{
			name:   "unknown token",
			mutate: func(_ *testing.T, env *multiEnv) { env.token = "mm_not_a_real_token" },
			want:   agent.ErrTokenRejected,
		},
		{
			name: "disabled agent",
			mutate: func(t *testing.T, env *multiEnv) {
				ag, err := env.data.GetAgent(context.Background(), env.agentID)
				if err != nil {
					t.Fatal(err)
				}
				ag.Status = store.AgentDisabled
				if err := env.data.UpdateAgent(context.Background(), ag); err != nil {
					t.Fatal(err)
				}
			},
			want: agent.ErrAgentDisabled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newMultiEnv(t, gateway.Options{})
			tt.mutate(t, env)
			cli := env.newAgent(io.Discard, agent.EndpointSpec{Kind: store.KindHTTP, LocalTarget: "127.0.0.1:1"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			err := cli.Run(ctx)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("agent retried instead of exiting")
			}
		})
	}
}

func TestStartupSweepRemovesOrphanedEphemeralEndpoints(t *testing.T) {
	env := newMultiEnv(t, gateway.Options{})
	ctx := context.Background()
	now := time.Now()
	for _, ep := range []*store.Endpoint{
		{ID: "ep_eph", AgentID: env.agentID, OrgID: "x", Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral, Subdomain: "eph", CreatedAt: now},
		{ID: "ep_res", AgentID: env.agentID, OrgID: "x", Kind: store.KindHTTP, Lifecycle: store.LifecycleReserved, Subdomain: "res", CreatedAt: now},
	} {
		ag, _ := env.data.GetAgent(ctx, env.agentID)
		ep.OrgID = ag.OrgID
		if err := env.data.CreateEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
	}
	gw := gateway.New(gateway.Options{Data: env.data, Conns: env.conns, Log: env.log})
	swept, err := gw.SweepOrphanedEphemeral(ctx)
	if err != nil || swept != 1 {
		t.Fatalf("swept = %d, %v", swept, err)
	}
	if _, err := env.data.GetEndpoint(ctx, "ep_eph"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ephemeral endpoint still present: %v", err)
	}
	if _, err := env.data.GetEndpoint(ctx, "ep_res"); err != nil {
		t.Fatalf("reserved endpoint removed: %v", err)
	}
}
