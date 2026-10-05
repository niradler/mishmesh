package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
)

func TestClusterNodesShareRateLimits(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", time.Minute)
	b := newNode(t, mr, "node-b", time.Minute)
	limit := ratelimit.Limit{Requests: 2, Period: time.Minute}
	ctx := context.Background()

	la, lb := a.cs.Limiter(nil), b.cs.Limiter(nil)
	if !la.Allow(ctx, "ep:1", limit).Allowed || !lb.Allow(ctx, "ep:1", limit).Allowed {
		t.Fatal("first two requests across nodes must pass")
	}
	if la.Allow(ctx, "ep:1", limit).Allowed || lb.Allow(ctx, "ep:1", limit).Allowed {
		t.Fatal("limit must be enforced cluster-wide")
	}
}
