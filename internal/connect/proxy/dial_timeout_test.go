package proxy

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDialTimeoutBoundsDNSResolution(t *testing.T) {
	g := DefaultGuard()
	g.lookup = func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := g.Dial(context.Background(), "slow.example.com:443", nil, 150*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from a hung resolver")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("dial took %s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dial ignored its timeout while resolving DNS")
	}
}
