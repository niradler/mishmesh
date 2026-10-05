package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/cluster"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/redis"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

var e2eClusterSecret = []byte("0123456789abcdef0123456789abcdef")

type lateResolver struct {
	cs atomic.Pointer[redis.ClusterConnStore]
}

func (l *lateResolver) LocalAgent(id string) (store.AgentConn, bool) {
	cs := l.cs.Load()
	if cs == nil {
		return nil, false
	}
	return cs.LocalAgent(id)
}

type clusterNode struct {
	name     string
	conns    *redis.ClusterConnStore
	apiURL   string
	ingress  *httptest.Server
	tcpHost  string
	tcpIngr  *ingress.TCP
	shutdown func()
}

func startClusterNode(t *testing.T, name, tcpHost string, data store.DataStore, mr *miniredis.Miniredis, portMin, portMax int) *clusterNode {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	late := &lateResolver{}
	relay := cluster.NewServer(cluster.ServerOptions{Secret: e2eClusterSecret, Local: late, Log: log})
	relayAddr, err := relay.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay.Shutdown)

	cs, err := redis.NewClusterConnStore(context.Background(), "redis://"+mr.Addr(), redis.ClusterOptions{
		NodeID:    name,
		Advertise: relayAddr.String(),
		Relay:     cluster.NewClient(e2eClusterSecret),
		Log:       log,
	})
	if err != nil {
		t.Fatal(err)
	}
	late.cs.Store(cs)
	t.Cleanup(func() { _ = cs.Close() })

	tcpIngr := ingress.NewTCP(ingress.TCPOptions{
		Conns:    cs,
		Data:     data,
		Log:      log,
		BindHost: tcpHost,
		PortMin:  portMin,
		PortMax:  portMax,
		Claims:   cs.PortClaims(portMin, portMax),
	})
	if err := tcpIngr.ListenCluster(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tcpIngr.Shutdown)

	gw := gateway.New(gateway.Options{Data: data, Conns: cs, Log: log, BaseDomain: "localhost", PublicScheme: "http", Ports: tcpIngr})
	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	ing := ingress.New(ingress.Options{Data: data, Conns: cs, Log: log, BaseDomain: "localhost"})
	ingSrv := httptest.NewServer(ing)
	t.Cleanup(ingSrv.Close)

	return &clusterNode{
		name:    name,
		conns:   cs,
		apiURL:  "ws" + strings.TrimPrefix(apiSrv.URL, "http"),
		ingress: ingSrv,
		tcpHost: tcpHost,
		tcpIngr: tcpIngr,
	}
}

func freePortRange(t *testing.T, hosts []string, width int) (int, int) {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		base := 20000 + rand.IntN(10000)
		var held []net.Listener
		ok := true
		for _, h := range hosts {
			for p := base; p < base+width && ok; p++ {
				ln, err := net.Listen("tcp", net.JoinHostPort(h, strconv.Itoa(p)))
				if err != nil {
					ok = false
					break
				}
				held = append(held, ln)
			}
		}
		for _, ln := range held {
			_ = ln.Close()
		}
		if ok {
			return base, base + width - 1
		}
	}
	t.Skip("no free port range on loopback hosts")
	return 0, 0
}

func startBackend(t *testing.T, label string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", label, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func startEcho(t *testing.T) string {
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

func pollBody(t *testing.T, srv *httptest.Server, host, path, want string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = host
		resp, err := srv.Client().Do(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			last = fmt.Sprintf("%d %s", resp.StatusCode, b)
			if resp.StatusCode == http.StatusOK && string(b) == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("host=%q path=%q never returned %q (last: %s)", host, path, want, last)
}

func echoOver(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = echoOnce(addr); lastErr == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("echo via %s: %v", addr, lastErr)
}

func echoOnce(addr string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		return err
	}
	buf := make([]byte, 4)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != "ping" {
		return fmt.Errorf("unexpected echo %q", buf)
	}
	return nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestClusterTwoNodes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cluster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	mr := miniredis.RunT(t)
	rawToken, agentID := seed(t, ctx, data)

	hostA, hostB := "127.0.0.1", "127.0.0.2"
	probe, err := net.Listen("tcp", hostB+":0")
	if err != nil {
		t.Skipf("loopback alias %s unavailable: %v", hostB, err)
	}
	_ = probe.Close()
	portMin, portMax := freePortRange(t, []string{hostA, hostB}, 4)

	nodeA := startClusterNode(t, "node-a", hostA, data, mr, portMin, portMax)
	nodeB := startClusterNode(t, "node-b", hostB, data, mr, portMin, portMax)

	backend1 := startBackend(t, "origin-one")
	backend2 := startBackend(t, "origin-two")
	echoAddr := startEcho(t)

	specs := func(backend *httptest.Server) []agent.EndpointSpec {
		return []agent.EndpointSpec{
			{Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral, Subdomain: "demo", LocalTarget: mustHost(t, backend.URL)},
			{Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, LocalTarget: echoAddr},
		}
	}

	agent1Ctx, cancelAgent1 := context.WithCancel(ctx)
	t.Cleanup(cancelAgent1)
	agent1 := agent.New(agent.Options{GatewayURL: nodeA.apiURL, Token: rawToken, Log: log, Endpoints: specs(backend1)})
	go func() { _ = agent1.Run(agent1Ctx) }()

	t.Run("http via the node the agent is not connected to", func(t *testing.T) {
		pollBody(t, nodeB.ingress, "demo.localhost", "/x", "origin-one /x")
		pollBody(t, nodeA.ingress, "demo.localhost", "/y", "origin-one /y")
	})

	var tcpPort int
	t.Run("tcp via both nodes", func(t *testing.T) {
		tcpPort = pollTCPPort(t, ctx, data, agentID)
		echoOver(t, net.JoinHostPort(nodeB.tcpHost, strconv.Itoa(tcpPort)))
		echoOver(t, net.JoinHostPort(nodeA.tcpHost, strconv.Itoa(tcpPort)))
	})

	t.Run("reconnect to the other node kicks the stale session and keeps the endpoint", func(t *testing.T) {
		agent2Ctx, cancelAgent2 := context.WithCancel(ctx)
		t.Cleanup(cancelAgent2)
		agent2 := agent.New(agent.Options{GatewayURL: nodeB.apiURL, Token: rawToken, Log: log, Endpoints: specs(backend2)})
		go func() { _ = agent2.Run(agent2Ctx) }()

		waitFor(t, "node A to drop the stale session", func() bool {
			_, ok := nodeA.conns.LocalAgent(agentID)
			return !ok
		})
		cancelAgent1()

		waitFor(t, "node B to own the agent", func() bool {
			_, ok := nodeB.conns.LocalAgent(agentID)
			return ok
		})
		if !nodeA.conns.OwnedElsewhere(agentID) {
			t.Fatal("node A should see the agent owned by node B")
		}

		time.Sleep(300 * time.Millisecond)
		if _, err := data.GetEndpointBySubdomain(ctx, "demo"); err != nil {
			t.Fatalf("endpoint deleted by stale node cleanup: %v", err)
		}

		pollBody(t, nodeA.ingress, "demo.localhost", "/z", "origin-two /z")
		pollBody(t, nodeB.ingress, "demo.localhost", "/z", "origin-two /z")

		var newPort int
		waitFor(t, "agent to register a tcp endpoint on node B", func() bool {
			eps, _ := data.ListEndpointsByAgent(ctx, agentID)
			for _, ep := range eps {
				if ep.Kind == store.KindTCP && ep.Port != tcpPort {
					newPort = ep.Port
					return true
				}
			}
			return false
		})
		echoOver(t, net.JoinHostPort(nodeA.tcpHost, strconv.Itoa(newPort)))
		echoOver(t, net.JoinHostPort(nodeB.tcpHost, strconv.Itoa(newPort)))
	})
}
