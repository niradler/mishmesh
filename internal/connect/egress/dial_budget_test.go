package egress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type slowDialer struct{ perAttempt time.Duration }

func (d slowDialer) Dial(ctx context.Context, _ string, _ *store.OriginTLS, _ time.Duration) (net.Conn, error) {
	select {
	case <-time.After(d.perAttempt):
		return nil, errors.New("unreachable")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (slowDialer) ResolveAll(context.Context, string) ([]string, error) { return nil, nil }

func TestEgressDialBoundsTotalFallbackTime(t *testing.T) {
	h := New(Options{Dialer: slowDialer{perAttempt: 50 * time.Millisecond}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h.dialBudget = 200 * time.Millisecond
	targets := make([]string, 20)
	for i := range targets {
		targets[i] = "192.0.2.1:443"
	}
	start := time.Now()
	_, ref := h.dial(context.Background(), &store.Agent{ID: "a"}, nil, "example.com:443", targets)
	if ref == nil {
		t.Fatal("expected a refusal")
	}
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("fallback took %s, want about the 200ms budget", took)
	}
}
