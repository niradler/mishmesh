package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
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

func TestEndToEndTCPTunnel(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	data, err := sqlite.Open(filepath.Join(t.TempDir(), "tcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	rawToken, agentID := seed(t, ctx, data)

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echoLn.Close() })
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()

	tcpIng := ingress.NewTCP(ingress.TCPOptions{Conns: conns, Log: log, BindHost: "127.0.0.1", PortMin: 24000, PortMax: 24100})
	t.Cleanup(tcpIng.Shutdown)

	gw := gateway.New(gateway.Options{Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http", Ports: tcpIng})
	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	cli := agent.New(agent.Options{
		GatewayURL: "ws" + strings.TrimPrefix(apiSrv.URL, "http"),
		Token:      rawToken,
		Log:        log,
		Endpoints:  []agent.EndpointSpec{{Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, LocalTarget: echoLn.Addr().String()}},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Run(runCtx) }()

	port := pollTCPPort(t, ctx, data, agentID)

	var conn net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial public tcp port %d: %v", port, err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo: got %q", buf)
	}
}

func pollTCPPort(t *testing.T, ctx context.Context, data store.DataStore, agentID string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		eps, _ := data.ListEndpointsByAgent(ctx, agentID)
		for _, ep := range eps {
			if ep.Kind == store.KindTCP && ep.Port > 0 {
				return ep.Port
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("tcp endpoint not registered")
	return 0
}
