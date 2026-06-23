package proxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestProxyRoundTrip(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	defer backend.Close()
	go func() {
		c, err := backend.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		_, _ = c.Write(buf)
	}()

	data, err := sqlite.Open(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	defer data.Close()
	conns := memory.NewConnStore()
	ctx := context.Background()
	now := time.Now()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_default", Name: "d", CreatedAt: now})
	Register(ctx, data, conns, nil, true)

	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: AgentID, OrgID: "org_default", Kind: store.KindHTTP,
		Method: store.MethodProxy, Lifecycle: store.LifecycleReserved,
		Policy: &store.EndpointPolicy{ProxyTarget: backend.Addr().String()}, CreatedAt: now,
	}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	conns.BindEndpoint(ep.ID, AgentID)

	ac := newConn(data, nil, true)
	stream, err := ac.OpenStream(ctx, ep.ID, store.KindHTTP, nil)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := make([]byte, 4)
	if _, err := io.ReadFull(stream, out); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != "ping" {
		t.Fatalf("echo mismatch: %q", out)
	}
}

func TestGuardBlocks(t *testing.T) {
	c := newConn(nil, nil, false)
	for _, target := range []string{"169.254.169.254:80", "169.254.10.10:80", "127.0.0.1:80", "0.0.0.0:80"} {
		if _, err := c.resolveTarget(target); err == nil {
			t.Fatalf("expected %q to be blocked", target)
		}
	}
	if _, err := c.resolveTarget("10.1.2.3:80"); err != nil {
		t.Fatalf("private LAN target should be allowed (feature intent): %v", err)
	}
	loop := newConn(nil, nil, true)
	if addr, err := loop.resolveTarget("127.0.0.1:80"); err != nil || addr != "127.0.0.1:80" {
		t.Fatalf("loopback should be allowed when opted in: addr=%q err=%v", addr, err)
	}
}
