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
	loopbackGuard, err := NewGuard(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	Register(ctx, data, conns, nil, loopbackGuard)

	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: AgentID, OrgID: "org_default", Kind: store.KindHTTP,
		Method: store.MethodProxy, Lifecycle: store.LifecycleReserved,
		Policy: &store.EndpointPolicy{ProxyTarget: backend.Addr().String()}, CreatedAt: now,
	}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	conns.BindEndpoint(ep.ID, AgentID)

	ac := newConn(data, nil, loopbackGuard)
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
