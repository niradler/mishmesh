package ratelimit

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestMemoryFloodDoesNotResetLimitedClient(t *testing.T) {
	h := newMemoryHarness()
	m := h.limiter.(*Memory)
	limit := Limit{Requests: 5, Period: time.Hour}
	ctx := context.Background()
	for i := 0; i < limit.Requests; i++ {
		if !m.Allow(ctx, "victim", limit).Allowed {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if m.Allow(ctx, "victim", limit).Allowed {
		t.Fatal("victim should be limited before the flood")
	}
	for i := 0; i < 3*defaultMaxKeys; i++ {
		m.Allow(ctx, "attacker-"+strconv.Itoa(i), limit)
		h.advance(time.Microsecond)
	}
	if m.Allow(ctx, "victim", limit).Allowed {
		t.Fatal("unique-key flood reset the victim's limit")
	}
	if got := m.size(); got > defaultMaxKeys {
		t.Fatalf("map grew past cap: %d", got)
	}
}

func BenchmarkMemoryAllowParallel(b *testing.B) {
	m := NewMemory()
	limit := Limit{Requests: 1000, Period: time.Second}
	ctx := context.Background()
	keys := make([]string, 4096)
	for i := range keys {
		keys[i] = "k" + strconv.Itoa(i)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			m.Allow(ctx, keys[i%len(keys)], limit)
			i++
		}
	})
}

func BenchmarkMemoryAllowFlood(b *testing.B) {
	m := NewMemory()
	limit := Limit{Requests: 5, Period: time.Hour}
	ctx := context.Background()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		m.Allow(ctx, "f"+strconv.Itoa(i), limit)
		i++
	}
}
