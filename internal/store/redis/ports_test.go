package redis

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestPortClaimsShouldHoldOnePortPerEndpointUnderConcurrentClaims(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)
	claims := []*PortClaims{a.cs.PortClaims(20000, 20999), b.cs.PortClaims(20000, 20999)}
	ctx := context.Background()

	const callers = 40
	results := make([]int, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = claims[i%2].Claim(ctx, "ep-shared", 0)
		}()
	}
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("claim %d: %v", i, errs[i])
		}
	}
	held := 0
	for _, key := range mr.Keys() {
		if len(key) > len(portKeyPrefix) && key[:len(portKeyPrefix)] == portKeyPrefix {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("endpoint holds %d port claims, want exactly 1", held)
	}
	recorded, err := mr.Get(endpointPortKeyPrefix + "ep-shared")
	if err != nil {
		t.Fatal(err)
	}
	for i, port := range results {
		if fmt.Sprint(port) != recorded {
			t.Fatalf("claim %d returned port %d but the recorded claim is %s", i, port, recorded)
		}
	}
}

type roundTripCounter struct{ n atomic.Int64 }

func (c *roundTripCounter) DialHook(next goredis.DialHook) goredis.DialHook { return next }

func (c *roundTripCounter) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		c.n.Add(1)
		return next(ctx, cmd)
	}
}

func (c *roundTripCounter) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		c.n.Add(1)
		return next(ctx, cmds)
	}
}

func TestPortClaimsShouldScanFullRangeInOneRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	claims := a.cs.PortClaims(20000, 20199)
	ctx := context.Background()
	for i := 0; i < 199; i++ {
		if _, err := claims.Claim(ctx, fmt.Sprintf("ep%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}

	counter := &roundTripCounter{}
	claims.rdb.AddHook(counter)
	port, err := claims.Claim(ctx, "ep-last", 0)
	if err != nil || port != 20199 {
		t.Fatalf("last claim = %d, %v", port, err)
	}
	t.Logf("client round trips for scanning claim: %d", counter.n.Load())
	if counter.n.Load() > 2 {
		t.Fatalf("claim used %d round trips, want a constant number", counter.n.Load())
	}
}
