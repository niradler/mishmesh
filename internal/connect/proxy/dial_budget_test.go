package proxy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestOpenStreamBoundsTotalFallbackTime(t *testing.T) {
	data, err := sqlite.Open(t.TempDir() + "/b.db")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	ctx := context.Background()
	now := time.Now()
	newConn(data, nil, nil).ensureAgent(ctx)
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_default", Name: "d", CreatedAt: now})
	targets := make([]string, 16)
	for i := range targets {
		targets[i] = "slow.example.com:443"
	}
	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: AgentID, OrgID: "org_default", Kind: store.KindHTTP,
		Method: store.MethodProxy, Lifecycle: store.LifecycleReserved,
		Policy: &store.EndpointPolicy{ProxyTargets: targets}, CreatedAt: now,
	}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	guard, err := NewGuard(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	guard.lookup = func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		select {
		case <-time.After(50 * time.Millisecond):
			return nil, errors.New("nxdomain")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := newConn(data, nil, guard)
	c.dialBudget = 200 * time.Millisecond
	start := time.Now()
	if _, err := c.OpenStream(ctx, ep.ID, store.KindHTTP, nil); err == nil {
		t.Fatal("expected failure")
	}
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("fallback took %s, want about the 200ms budget", took)
	}
}
