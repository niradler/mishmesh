package redis

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

type usageFixture struct {
	mr    *miniredis.Miniredis
	store *ConnStore
	clock *fakeClock
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	mr := miniredis.RunT(t)
	s := newWithClient(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}))
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s.usage.now = clock.now
	s.usage.flushInterval = time.Hour
	t.Cleanup(func() { _ = s.Close() })
	return &usageFixture{mr: mr, store: s, clock: clock}
}

func (f *usageFixture) redisValue(t *testing.T, org string) int64 {
	t.Helper()
	v, err := f.mr.Get(usageKeyPrefix + org)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddUsageDoesNotTouchRedisSynchronously(t *testing.T) {
	f := newUsageFixture(t)
	before := f.mr.CommandCount()
	for range 1000 {
		f.store.AddUsage("org_1", 10)
	}
	if got := f.mr.CommandCount(); got != before {
		t.Fatalf("AddUsage issued %d redis commands, want 0", got-before)
	}
	if got := f.redisValue(t, "org_1"); got != 0 {
		t.Fatalf("redis value before flush = %d, want 0", got)
	}
	f.store.usage.flush(time.Second)
	if got := f.redisValue(t, "org_1"); got != 10000 {
		t.Fatalf("redis value after flush = %d, want 10000", got)
	}
}

func TestUsageAggregatesPerOrg(t *testing.T) {
	f := newUsageFixture(t)
	f.store.AddUsage("org_a", 5)
	f.store.AddUsage("org_b", 7)
	f.store.AddUsage("org_a", 6)
	f.store.usage.flush(time.Second)
	tests := []struct {
		org  string
		want int64
	}{{"org_a", 11}, {"org_b", 7}}
	for _, tt := range tests {
		if got := f.redisValue(t, tt.org); got != tt.want {
			t.Errorf("%s redis = %d, want %d", tt.org, got, tt.want)
		}
	}
}

func TestUsageReadIsCachedWithinTTL(t *testing.T) {
	f := newUsageFixture(t)
	if err := f.mr.Set(usageKeyPrefix+"org_1", "1000"); err != nil {
		t.Fatal(err)
	}
	if got := f.store.Usage("org_1"); got != 1000 {
		t.Fatalf("first read = %d, want 1000", got)
	}
	if err := f.mr.Set(usageKeyPrefix+"org_1", "5000"); err != nil {
		t.Fatal(err)
	}
	before := f.mr.CommandCount()
	for range 500 {
		if got := f.store.Usage("org_1"); got != 1000 {
			t.Fatalf("cached read = %d, want 1000", got)
		}
	}
	if got := f.mr.CommandCount(); got != before {
		t.Fatalf("cached reads issued %d redis commands, want 0", got-before)
	}
	f.clock.advance(usageCacheTTL + time.Millisecond)
	if got := f.store.Usage("org_1"); got != 5000 {
		t.Fatalf("read after ttl = %d, want 5000", got)
	}
}

func TestUsageIncludesLocalUnflushedBytes(t *testing.T) {
	f := newUsageFixture(t)
	if err := f.mr.Set(usageKeyPrefix+"org_1", "100"); err != nil {
		t.Fatal(err)
	}
	_ = f.store.Usage("org_1")
	f.store.AddUsage("org_1", 40)
	if got := f.store.Usage("org_1"); got != 140 {
		t.Fatalf("usage with pending = %d, want 140", got)
	}
	f.store.usage.flush(time.Second)
	if got := f.store.Usage("org_1"); got != 140 {
		t.Fatalf("usage after flush = %d, want 140 (no double count)", got)
	}
	if got := f.redisValue(t, "org_1"); got != 140 {
		t.Fatalf("redis after flush = %d, want 140", got)
	}
}

func TestUsageMissingKeyIsZero(t *testing.T) {
	f := newUsageFixture(t)
	if got := f.store.Usage("never_seen"); got != 0 {
		t.Fatalf("usage = %d, want 0", got)
	}
}

func TestCloseFlushesPendingUsage(t *testing.T) {
	mr := miniredis.RunT(t)
	s := newWithClient(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}))
	s.usage.flushInterval = time.Hour
	s.AddUsage("org_1", 123)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	v, err := mr.Get(usageKeyPrefix + "org_1")
	if err != nil || v != "123" {
		t.Fatalf("redis after close = %q err=%v, want 123", v, err)
	}
}

func TestBackgroundLoopFlushes(t *testing.T) {
	mr := miniredis.RunT(t)
	s := newWithClient(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}))
	s.usage.flushInterval = 20 * time.Millisecond
	t.Cleanup(func() { _ = s.Close() })
	s.AddUsage("org_1", 9)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, err := mr.Get(usageKeyPrefix + "org_1"); err == nil && v == "9" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background flush never wrote usage to redis")
}

func TestRedisOutageKeepsPendingAndFallsBackToLocal(t *testing.T) {
	f := newUsageFixture(t)
	f.store.AddUsage("org_1", 50)
	f.mr.Close()
	f.store.usage.flush(200 * time.Millisecond)
	if got := f.store.usage.org("org_1").pending.Load(); got != 50 {
		t.Fatalf("pending after failed flush = %d, want 50 retained", got)
	}
	if got := f.store.Usage("org_1"); got != 50 {
		t.Fatalf("usage during outage = %d, want local 50", got)
	}
}

func TestUsageWarningsAreRateLimited(t *testing.T) {
	f := newUsageFixture(t)
	for range 100 {
		f.store.usage.warn("test warning")
	}
	if got := f.store.usage.warnSuppressed; got != 99 {
		t.Fatalf("suppressed = %d, want 99", got)
	}
	f.clock.advance(usageWarnInterval + time.Second)
	f.store.usage.warn("test warning")
	if got := f.store.usage.warnSuppressed; got != 0 {
		t.Fatalf("suppressed after interval = %d, want 0", got)
	}
}

func TestUsageConcurrentAddAndFlush(t *testing.T) {
	f := newUsageFixture(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				f.store.AddUsage("org_1", 1)
				_ = f.store.Usage("org_1")
			}
		}()
	}
	for range 5 {
		f.store.usage.flush(time.Second)
	}
	wg.Wait()
	f.store.usage.flush(time.Second)
	if got := f.redisValue(t, "org_1"); got != 8000 {
		t.Fatalf("redis total = %d, want 8000", got)
	}
}
