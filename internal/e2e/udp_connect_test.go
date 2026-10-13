package e2e

import (
	"context"
	"encoding/json"
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
	"github.com/mishmesh/mishmesh/internal/connect/bridge"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type connectHarness struct {
	t          *testing.T
	ctx        context.Context
	log        *slog.Logger
	data       store.DataStore
	conns      store.ConnectionStore
	gatewayURL string
}

func newConnectHarness(t *testing.T) *connectHarness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "connect.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()

	udpIng := ingress.NewUDP(ingress.UDPOptions{Conns: conns, Data: data, Log: log, BindHost: "127.0.0.1", PortMin: 24200, PortMax: 24300, Idle: 2 * time.Second})
	t.Cleanup(udpIng.Shutdown)

	gw := gateway.New(gateway.Options{
		Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http",
		UDPPorts: udpIng,
		Connect:  bridge.New(bridge.Options{Data: data, Conns: conns, Log: log}),
	})
	mux := http.NewServeMux()
	mux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &connectHarness{t: t, ctx: ctx, log: log, data: data, conns: conns, gatewayURL: "ws" + strings.TrimPrefix(srv.URL, "http")}
}

func (h *connectHarness) org() string {
	h.t.Helper()
	org := &store.Org{ID: store.NewID("org"), Name: "o", CreatedAt: time.Now()}
	if err := h.data.CreateOrg(h.ctx, org); err != nil {
		h.t.Fatal(err)
	}
	return org.ID
}

func (h *connectHarness) agent(orgID, name string) (token, agentID string) {
	h.t.Helper()
	ag := &store.Agent{ID: store.NewID("ag"), OrgID: orgID, Name: name, Status: store.AgentActive, CreatedAt: time.Now()}
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
	return raw, ag.ID
}

func (h *connectHarness) run(token string, endpoints []agent.EndpointSpec, connects []agent.ConnectSpec) {
	h.t.Helper()
	cli := agent.New(agent.Options{GatewayURL: h.gatewayURL, Token: token, Log: h.log, Out: io.Discard, Endpoints: endpoints, Connects: connects})
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
}

func (h *connectHarness) waitEndpoint(agentID string, match func(*store.Endpoint) bool) *store.Endpoint {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		eps, _ := h.data.ListEndpointsByAgent(h.ctx, agentID)
		for _, ep := range eps {
			if _, online := h.conns.ResolveEndpoint(ep.ID); online && match(ep) {
				return ep
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("endpoint for agent %s not registered", agentID)
	return nil
}

func udpEchoServer(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, tunnel.MaxDatagramSize)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			_, _ = pc.WriteTo(append([]byte("echo:"), buf[:n]...), addr)
		}
	}()
	return pc.LocalAddr().String()
}

func tcpEchoServer(t *testing.T) string {
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

func freeAddr(t *testing.T, network string) string {
	t.Helper()
	if network == "udp" {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		return pc.LocalAddr().String()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func udpRoundTrip(c net.Conn, msg string, wait time.Duration) (string, error) {
	if _, err := c.Write([]byte(msg)); err != nil {
		return "", err
	}
	buf := make([]byte, 2048)
	_ = c.SetReadDeadline(time.Now().Add(wait))
	n, err := c.Read(buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func awaitUDPEcho(t *testing.T, addr, msg string) net.Conn {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	deadline := time.Now().Add(8 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := udpRoundTrip(c, msg, 300*time.Millisecond)
		if err == nil && got == "echo:"+msg {
			return c
		}
		lastErr = fmt.Errorf("got %q, err %v", got, err)
	}
	t.Fatalf("no udp echo via %s: %v", addr, lastErr)
	return nil
}

func tcpRoundTrip(addr, msg string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if _, err := c.Write([]byte(msg)); err != nil {
		return "", err
	}
	buf := make([]byte, len(msg))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func connectPolicy(allow ...string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"connect": map[string]any{"allow_agents": allow}})
	return raw
}

func TestEndToEndUDPPublicPort(t *testing.T) {
	h := newConnectHarness(t)
	org := h.org()
	token, agentID := h.agent(org, "origin")
	h.run(token, []agent.EndpointSpec{{Name: "dns", Kind: store.KindUDP, Lifecycle: store.LifecycleEphemeral, LocalTarget: udpEchoServer(t)}}, nil)

	ep := h.waitEndpoint(agentID, func(ep *store.Endpoint) bool { return ep.Kind == store.KindUDP && ep.Port > 0 })
	public := fmt.Sprintf("127.0.0.1:%d", ep.Port)
	awaitUDPEcho(t, public, "warmup")

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			c, err := net.Dial("udp", public)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			for j := range 5 {
				msg := fmt.Sprintf("client%d-%d", i, j)
				if got, err := udpRoundTrip(c, msg, 3*time.Second); err != nil || got != "echo:"+msg {
					t.Errorf("client %d: got %q, err %v", i, got, err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestEndToEndAgentConnectUDP(t *testing.T) {
	h := newConnectHarness(t)
	org := h.org()
	originToken, originID := h.agent(org, "office")
	entryToken, _ := h.agent(org, "laptop")

	h.run(originToken, []agent.EndpointSpec{{
		Name: "office-vpn", Kind: store.KindUDP, Lifecycle: store.LifecycleEphemeral, Private: true,
		LocalTarget: udpEchoServer(t), Policy: connectPolicy("laptop"),
	}}, nil)
	ep := h.waitEndpoint(originID, func(ep *store.Endpoint) bool { return ep.Kind == store.KindUDP })
	if ep.Port != 0 {
		t.Fatalf("private endpoint got public port %d", ep.Port)
	}
	if ep.Policy == nil || ep.Policy.Connect == nil || ep.Policy.Connect.Name != "office-vpn" {
		t.Fatalf("connect name not defaulted from tunnel name: %+v", ep.Policy)
	}

	listen := freeAddr(t, "udp")
	h.run(entryToken, nil, []agent.ConnectSpec{{Name: "vpn", Endpoint: "office-vpn", Kind: store.KindUDP, Listen: listen, Idle: 5 * time.Second}})

	c := awaitUDPEcho(t, listen, "handshake")
	for i := range 20 {
		msg := fmt.Sprintf("packet-%d", i)
		if got, err := udpRoundTrip(c, msg, 3*time.Second); err != nil || got != "echo:"+msg {
			t.Fatalf("packet %d: got %q, err %v", i, got, err)
		}
	}
}

func TestEndToEndAgentConnectTCP(t *testing.T) {
	h := newConnectHarness(t)
	org := h.org()
	originToken, originID := h.agent(org, "db-host")
	entryToken, _ := h.agent(org, "app-host")

	h.run(originToken, []agent.EndpointSpec{{
		Name: "postgres", Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, Private: true,
		LocalTarget: tcpEchoServer(t), Policy: connectPolicy(store.ConnectAnyAgent),
	}}, nil)
	h.waitEndpoint(originID, func(ep *store.Endpoint) bool { return ep.Kind == store.KindTCP })

	listen := freeAddr(t, "tcp")
	h.run(entryToken, nil, []agent.ConnectSpec{{Name: "pg", Endpoint: "postgres", Kind: store.KindTCP, Listen: listen}})

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := tcpRoundTrip(listen, "select 1")
		if err == nil && got == "select 1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tcp connect: got %q, err %v", got, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEndToEndAgentConnectDenied(t *testing.T) {
	h := newConnectHarness(t)
	org := h.org()
	otherOrg := h.org()
	originToken, originID := h.agent(org, "office")
	allowedToken, _ := h.agent(org, "trusted")
	strangerToken, _ := h.agent(org, "stranger")
	outsiderToken, _ := h.agent(otherOrg, "trusted")

	h.run(originToken, []agent.EndpointSpec{
		{Name: "only-trusted", Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, Private: true, LocalTarget: tcpEchoServer(t), Policy: connectPolicy("trusted")},
		{Name: "no-policy", Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, Private: true, LocalTarget: tcpEchoServer(t)},
	}, nil)
	h.waitEndpoint(originID, func(ep *store.Endpoint) bool { return connectNameOf(ep) == "only-trusted" })
	h.waitEndpoint(originID, func(ep *store.Endpoint) bool { return connectNameOf(ep) == "no-policy" })

	allowed := freeAddr(t, "tcp")
	h.run(allowedToken, nil, []agent.ConnectSpec{{Name: "ok", Endpoint: "only-trusted", Kind: store.KindTCP, Listen: allowed}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, err := tcpRoundTrip(allowed, "hi"); err == nil && got == "hi" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("allowed agent never connected")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cases := []struct {
		name     string
		token    string
		endpoint string
		kind     string
	}{
		{"agent not on allow list", strangerToken, "only-trusted", store.KindTCP},
		{"same name in another org", outsiderToken, "only-trusted", store.KindTCP},
		{"endpoint without connect policy", allowedToken, "no-policy", store.KindTCP},
		{"unknown endpoint", allowedToken, "missing", store.KindTCP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listen := freeAddr(t, "tcp")
			h.run(tc.token, nil, []agent.ConnectSpec{{Name: "x", Endpoint: tc.endpoint, Kind: tc.kind, Listen: listen}})
			time.Sleep(300 * time.Millisecond)
			for range 3 {
				if got, err := tcpRoundTrip(listen, "hi"); err == nil {
					t.Fatalf("connect should be refused, got echo %q", got)
				}
			}
		})
	}
}

func connectNameOf(ep *store.Endpoint) string {
	if ep.Policy == nil || ep.Policy.Connect == nil {
		return ""
	}
	return ep.Policy.Connect.Name
}

func TestEndToEndAgentConnectDirectProxyOrigin(t *testing.T) {
	h := newConnectHarness(t)
	org := h.org()
	entryToken, _ := h.agent(org, "branch")

	guard, err := proxy.NewGuard(true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: proxy.AgentID, OrgID: org, Kind: store.KindUDP, Method: store.MethodProxy,
		Lifecycle: store.LifecycleReserved, CreatedAt: time.Now(),
		Policy: &store.EndpointPolicy{
			ProxyTargets: []string{"udp://" + udpEchoServer(t)},
			Connect:      &store.ConnectPolicy{Name: "hq-vpn", AllowAgents: []string{store.ConnectAnyAgent}},
		},
	}
	proxy.Register(h.ctx, h.data, h.conns, h.log, guard)
	if err := h.data.CreateEndpoint(h.ctx, ep); err != nil {
		t.Fatal(err)
	}
	h.conns.BindEndpoint(ep.ID, proxy.AgentID)

	listen := freeAddr(t, "udp")
	h.run(entryToken, nil, []agent.ConnectSpec{{Name: "vpn", Endpoint: "hq-vpn", Kind: store.KindUDP, Listen: listen}})
	c := awaitUDPEcho(t, listen, "direct")
	if got, err := udpRoundTrip(c, "again", 3*time.Second); err != nil || got != "echo:again" {
		t.Fatalf("got %q, err %v", got, err)
	}
}
