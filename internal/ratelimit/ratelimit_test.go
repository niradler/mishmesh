package ratelimit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

type harness struct {
	name    string
	limiter Limiter
	advance func(time.Duration)
}

func newMemoryHarness() harness {
	now := time.Unix(1_700_000_000, 0)
	m := NewMemory()
	m.now = func() time.Time { return now }
	return harness{"memory", m, func(d time.Duration) { now = now.Add(d) }}
}

func newRedisHarness(t *testing.T) harness {
	t.Helper()
	mr := miniredis.RunT(t)
	now := time.Unix(1_700_000_000, 0)
	mr.SetTime(now)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return harness{"redis", NewRedis(rdb, nil), func(d time.Duration) {
		now = now.Add(d)
		mr.SetTime(now)
	}}
}

func harnesses(t *testing.T) []harness {
	return []harness{newMemoryHarness(), newRedisHarness(t)}
}

func TestBurstThenDeny(t *testing.T) {
	limit := Limit{Requests: 2, Period: time.Second, Burst: 3}
	for _, h := range harnesses(t) {
		t.Run(h.name, func(t *testing.T) {
			ctx := context.Background()
			for i := 0; i < 3; i++ {
				if !h.limiter.Allow(ctx, "k", limit).Allowed {
					t.Fatalf("request %d within burst denied", i)
				}
			}
			d := h.limiter.Allow(ctx, "k", limit)
			if d.Allowed {
				t.Fatal("request beyond burst allowed")
			}
			if d.RetryAfter <= 0 || d.RetryAfter > time.Second {
				t.Fatalf("retry after %v out of range", d.RetryAfter)
			}
		})
	}
}

func TestRefillOverTime(t *testing.T) {
	limit := Limit{Requests: 2, Period: time.Second}
	for _, h := range harnesses(t) {
		t.Run(h.name, func(t *testing.T) {
			ctx := context.Background()
			h.limiter.Allow(ctx, "k", limit)
			h.limiter.Allow(ctx, "k", limit)
			if h.limiter.Allow(ctx, "k", limit).Allowed {
				t.Fatal("expected deny when empty")
			}
			h.advance(500 * time.Millisecond)
			if !h.limiter.Allow(ctx, "k", limit).Allowed {
				t.Fatal("expected allow after one token refilled")
			}
			if h.limiter.Allow(ctx, "k", limit).Allowed {
				t.Fatal("expected deny, only one token refilled")
			}
			h.advance(time.Hour)
			for i := 0; i < 2; i++ {
				if !h.limiter.Allow(ctx, "k", limit).Allowed {
					t.Fatalf("request %d after long idle denied", i)
				}
			}
			if h.limiter.Allow(ctx, "k", limit).Allowed {
				t.Fatal("refill must cap at capacity")
			}
		})
	}
}

func TestKeysAreIndependent(t *testing.T) {
	limit := Limit{Requests: 1, Period: time.Minute}
	for _, h := range harnesses(t) {
		t.Run(h.name, func(t *testing.T) {
			ctx := context.Background()
			if !h.limiter.Allow(ctx, "a", limit).Allowed {
				t.Fatal("a first denied")
			}
			if h.limiter.Allow(ctx, "a", limit).Allowed {
				t.Fatal("a second allowed")
			}
			if !h.limiter.Allow(ctx, "b", limit).Allowed {
				t.Fatal("b first denied")
			}
		})
	}
}

func TestInvalidLimitAllows(t *testing.T) {
	for _, h := range harnesses(t) {
		t.Run(h.name, func(t *testing.T) {
			if !h.limiter.Allow(context.Background(), "k", Limit{}).Allowed {
				t.Fatal("zero limit must not block")
			}
		})
	}
}

func TestRedisSharedAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	limit := Limit{Requests: 1, Period: time.Minute}
	newNode := func() *Redis {
		rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		return NewRedis(rdb, nil)
	}
	a, b := newNode(), newNode()
	ctx := context.Background()
	if !a.Allow(ctx, "shared", limit).Allowed {
		t.Fatal("first request denied")
	}
	if b.Allow(ctx, "shared", limit).Allowed {
		t.Fatal("second node must see the first node's consumption")
	}
}

func TestRedisFailsOpenAndWarns(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	var buf bytes.Buffer
	l := NewRedis(rdb, slog.New(slog.NewTextHandler(&buf, nil)))
	mr.Close()

	limit := Limit{Requests: 1, Period: time.Minute}
	for i := 0; i < 3; i++ {
		if !l.Allow(context.Background(), "k", limit).Allowed {
			t.Fatal("must fail open when redis is down")
		}
	}
	if got := strings.Count(buf.String(), "failing open"); got != 1 {
		t.Fatalf("expected exactly one throttled warning, got %d: %s", got, buf.String())
	}
}

func TestMemoryEvictsIdleBuckets(t *testing.T) {
	h := newMemoryHarness()
	m := h.limiter.(*Memory)
	m.maxKeys = 2
	limit := Limit{Requests: 1, Period: time.Second}
	ctx := context.Background()
	m.Allow(ctx, "a", limit)
	m.Allow(ctx, "b", limit)
	h.advance(time.Minute)
	m.Allow(ctx, "c", limit)
	if len(m.buckets) != 1 {
		t.Fatalf("expected idle buckets evicted, have %d", len(m.buckets))
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want int
	}{
		{0, 1},
		{time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{time.Minute, 60},
	}
	for _, c := range cases {
		if got := RetryAfterSeconds(c.in); got != c.want {
			t.Errorf("RetryAfterSeconds(%v)=%d want %d", c.in, got, c.want)
		}
	}
}
