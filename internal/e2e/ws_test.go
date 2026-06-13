package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestEndToEndWebSocketTunnel(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	data, err := sqlite.Open(t.TempDir() + "/ws.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	rawToken, _ := seed(t, ctx, data)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		connCtx := context.Background()
		for {
			typ, msg, err := c.Read(connCtx)
			if err != nil {
				return
			}
			if err := c.Write(connCtx, typ, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(backend.Close)

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
			Subdomain:   "ws",
			LocalTarget: mustHost(t, backend.URL),
		}},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = cli.Run(runCtx) }()

	ep := waitEndpoint(t, ctx, data, "ws")
	wsURL := "ws" + strings.TrimPrefix(ingSrv.URL, "http") + "/tunnel/" + ep.ID

	dialCtx, dialCancel := context.WithTimeout(ctx, 8*time.Second)
	defer dialCancel()

	var conn *websocket.Conn
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		c, _, err := websocket.Dial(dialCtx, wsURL, nil)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("websocket dial through tunnel never succeeded")
	}
	defer conn.CloseNow()

	if err := conn.Write(dialCtx, websocket.MessageText, []byte("ping-through-tunnel")); err != nil {
		t.Fatalf("ws write: %v", err)
	}
	typ, msg, err := conn.Read(dialCtx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if typ != websocket.MessageText || string(msg) != "ping-through-tunnel" {
		t.Fatalf("ws echo mismatch: typ=%v msg=%q", typ, msg)
	}
}

func waitEndpoint(t *testing.T, ctx context.Context, data store.DataStore, subdomain string) *store.Endpoint {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ep, err := data.GetEndpointBySubdomain(ctx, subdomain); err == nil {
			return ep
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("endpoint %q never registered", subdomain)
	return nil
}
