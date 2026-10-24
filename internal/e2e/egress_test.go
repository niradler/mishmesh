package e2e

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/connect/egress"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type accessRecorder struct {
	mu     sync.Mutex
	events []store.AccessEvent
}

func (r *accessRecorder) Record(e *store.AccessEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *e)
}

func (r *accessRecorder) waitFor(t *testing.T, match func(store.AccessEvent) bool) store.AccessEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, e := range r.events {
			if match(e) {
				r.mu.Unlock()
				return e
			}
		}
		r.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected access event not recorded")
	return store.AccessEvent{}
}

type egressHarness struct {
	t       *testing.T
	ctx     context.Context
	log     *slog.Logger
	data    store.DataStore
	access  *accessRecorder
	gateway string
}

func newEgressHarness(t *testing.T, allowLoopback bool) *egressHarness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "egress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	cp := controlplane.New(data, conns, "", log)
	guard, err := proxy.NewGuard(allowLoopback, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	access := &accessRecorder{}
	gw := gateway.New(gateway.Options{
		Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http",
		Egress: egress.New(egress.Options{Conns: conns, Log: log, Deciders: cp, Dialer: guard, Access: access}),
	})
	mux := http.NewServeMux()
	mux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &egressHarness{t: t, ctx: ctx, log: log, data: data, access: access, gateway: "ws" + strings.TrimPrefix(srv.URL, "http")}
}

func (h *egressHarness) orgWithPolicy(cedarSrc string) string {
	h.t.Helper()
	org := &store.Org{ID: store.NewID("org"), Name: "o", CreatedAt: time.Now()}
	if err := h.data.CreateOrg(h.ctx, org); err != nil {
		h.t.Fatal(err)
	}
	if cedarSrc != "" {
		if err := h.data.SetOrgPolicy(h.ctx, &store.OrgPolicy{OrgID: org.ID, CedarSrc: cedarSrc, UpdatedAt: time.Now()}); err != nil {
			h.t.Fatal(err)
		}
	}
	return org.ID
}

func (h *egressHarness) runAgent(orgID string) string {
	h.t.Helper()
	ag := &store.Agent{ID: store.NewID("ag"), OrgID: orgID, Name: "exit", Status: store.AgentActive, CreatedAt: time.Now()}
	if err := h.data.CreateAgent(h.ctx, ag); err != nil {
		h.t.Fatal(err)
	}
	raw, hash, err := store.GenerateToken()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.data.CreateToken(h.ctx, &store.Token{ID: store.NewID("tok"), OrgID: orgID, AgentID: ag.ID, Hash: hash, CreatedAt: time.Now()}); err != nil {
		h.t.Fatal(err)
	}
	listen := freeAddr(h.t, "tcp")
	cli := agent.New(agent.Options{GatewayURL: h.gateway, Token: raw, Log: h.log, Out: io.Discard, Egress: &agent.EgressSpec{Listen: listen}})
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cli.Run(ctx)
	}()
	h.t.Cleanup(func() {
		cancel()
		<-done
	})
	return listen
}

func egressPolicyFor(addr string) string {
	host, port, _ := net.SplitHostPort(addr)
	return fmt.Sprintf(`permit (principal, action == Action::"egress:connect", resource) when { resource.host == %q && resource.port == %s };`, host, port)
}

func proxyConnect(t *testing.T, proxyAddr, target string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", proxyAddr)
		if err == nil {
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
			br := bufio.NewReader(c)
			resp, rerr := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
			if rerr == nil && !(resp.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline)) {
				_ = c.SetDeadline(time.Time{})
				t.Cleanup(func() { _ = c.Close() })
				return c, br, resp.StatusCode
			}
			_ = c.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy %s never answered CONNECT: %v", proxyAddr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEgressConnectAllowedEchoes(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	listen := h.runAgent(h.orgWithPolicy(egressPolicyFor(echo)))

	c, br, status := proxyConnect(t, listen, echo)
	if status != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", status)
	}
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	_ = c.Close()
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Source == store.AccessSourceEgress })
	if ev.Decision != store.AccessAllow || ev.Destination != echo || len(ev.PolicyIDs) == 0 {
		t.Fatalf("access event = %+v", ev)
	}
}

func TestEgressAbsoluteFormReachesBackend(t *testing.T) {
	h := newEgressHarness(t, true)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			http.Error(w, "proxy header leaked", http.StatusTeapot)
			return
		}
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}))
	t.Cleanup(backend.Close)
	addr := strings.TrimPrefix(backend.URL, "http://")
	listen := h.runAgent(h.orgWithPolicy(egressPolicyFor(addr)))
	proxyConnect(t, listen, addr)

	proxyURL, _ := url.Parse("http://" + listen)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, _ := http.NewRequest(http.MethodGet, backend.URL+"/via-proxy", nil)
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello /via-proxy" {
		t.Fatalf("absolute-form = %d %q", resp.StatusCode, body)
	}
}

func TestEgressDeniedByPolicy(t *testing.T) {
	h := newEgressHarness(t, true)
	allowed := tcpEchoServer(t)
	other := tcpEchoServer(t)
	listen := h.runAgent(h.orgWithPolicy(egressPolicyFor(allowed)))

	if _, _, status := proxyConnect(t, listen, other); status != http.StatusForbidden {
		t.Fatalf("denied CONNECT status = %d, want 403", status)
	}
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == other })
	if ev.Decision != store.AccessDeny {
		t.Fatalf("access event = %+v", ev)
	}
}

func TestEgressDefaultPolicyDenies(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	listen := h.runAgent(h.orgWithPolicy(""))
	if _, _, status := proxyConnect(t, listen, echo); status != http.StatusForbidden {
		t.Fatalf("default policy CONNECT status = %d, want 403", status)
	}
}

func TestEgressGuardBlocksInternalTargets(t *testing.T) {
	h := newEgressHarness(t, false)
	echo := tcpEchoServer(t)
	_, port, _ := net.SplitHostPort(echo)
	policy := fmt.Sprintf(`permit (principal, action == Action::"egress:connect", resource) when { resource.port == %s };`, port)
	listen := h.runAgent(h.orgWithPolicy(policy))

	for _, target := range []string{echo, net.JoinHostPort("0.0.0.0", port), net.JoinHostPort("localhost", port)} {
		if _, _, status := proxyConnect(t, listen, target); status != http.StatusForbidden {
			t.Fatalf("CONNECT %s status = %d, want 403", target, status)
		}
	}
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Reason == "blocked_destination" })
	if ev.Decision != store.AccessDeny {
		t.Fatalf("access event = %+v", ev)
	}
}

func TestEgressRejectsSNIMismatch(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	listen := h.runAgent(h.orgWithPolicy(egressPolicyFor(echo)))

	c, _, status := proxyConnect(t, listen, echo)
	if status != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", status)
	}
	tc := tls.Client(c, &tls.Config{ServerName: "evil.test"})
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err == nil {
		t.Fatal("tls handshake through mismatched SNI succeeded")
	}
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == echo })
	if ev.Decision != store.AccessDeny || ev.Reason != "sni_mismatch" {
		t.Fatalf("access event = %+v", ev)
	}
}
