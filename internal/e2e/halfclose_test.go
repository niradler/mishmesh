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

func TestTCPHalfCloseDeliversResponseAndMetersBothDirections(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	data, err := sqlite.Open(filepath.Join(t.TempDir(), "halfclose.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	rawToken, agentID := seed(t, ctx, data)
	ag, err := data.GetAgent(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	go func() {
		for {
			c, err := backend.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte("banner\n"))
				in, _ := io.ReadAll(c)
				_, _ = c.Write([]byte("echo:" + string(in)))
			}()
		}
	}()

	tcpIng := ingress.NewTCP(ingress.TCPOptions{Conns: conns, Data: data, Log: log, BindHost: "127.0.0.1", PortMin: 24400, PortMax: 24500})
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
		Endpoints:  []agent.EndpointSpec{{Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, LocalTarget: backend.Addr().String()}},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Run(runCtx) }()

	port := pollTCPPort(t, ctx, data, agentID)
	var conn *net.TCPConn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn = c.(*net.TCPConn)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("dial public tcp port")
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	const want = "banner\necho:hello"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}

	wantUsage := int64(len("hello") + len(want))
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && conns.Usage(ag.OrgID) < wantUsage {
		time.Sleep(20 * time.Millisecond)
	}
	if usage := conns.Usage(ag.OrgID); usage != wantUsage {
		t.Fatalf("usage = %d, want %d (up and down both metered)", usage, wantUsage)
	}
}
