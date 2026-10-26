package agent

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestAllowlistResolveHonoursContextCancellation(t *testing.T) {
	a := NewAllowlist([]string{"slow.example.com"})
	a.lookupIP = func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, _, ok := a.Resolve(ctx, "slow.example.com:443")
		done <- ok
	}()
	time.AfterFunc(100*time.Millisecond, cancel)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled resolve reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve did not return after the context was cancelled")
	}
}

func TestDialTargetHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := dialTarget(ctx, localTarget{addr: "192.0.2.1:9"}, "")
	if err == nil {
		t.Fatal("expected an error from a cancelled dial")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("dial took %s", time.Since(start))
	}
}
