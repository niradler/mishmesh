package gateway

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestOpenStreamHonorsContextOnStalledAgent(t *testing.T) {
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	sess, err := tunnel.Server(near)
	if err != nil {
		t.Fatal(err)
	}
	conn := newAgentConn("ag_1", "org_1", sess, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		c, err := conn.OpenStream(ctx, "ep_1", store.KindTCP, nil)
		done <- result{c, err}
	}()
	select {
	case res := <-done:
		if res.err == nil {
			_ = res.conn.Close()
			t.Fatal("expected an error from a stalled agent")
		}
		if !errors.Is(res.err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context deadline exceeded", res.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OpenStream ignored context cancellation on a stalled agent")
	}
}

func TestOpenStreamRejectsCancelledContext(t *testing.T) {
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	sess, err := tunnel.Server(near)
	if err != nil {
		t.Fatal(err)
	}
	conn := newAgentConn("ag_1", "org_1", sess, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conn.OpenStream(ctx, "ep_1", store.KindTCP, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}
