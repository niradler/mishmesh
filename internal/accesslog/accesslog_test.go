package accesslog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type fakeSink struct {
	mu      sync.Mutex
	events  []*store.AccessEvent
	batches int
	pruned  []time.Time
	fail    error
}

func (f *fakeSink) AppendAccessEvents(_ context.Context, events []*store.AccessEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.batches++
	f.events = append(f.events, events...)
	return nil
}

func (f *fakeSink) PruneAccessEvents(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned = append(f.pruned, before)
	return 0, nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func allow(org string) *store.AccessEvent {
	return &store.AccessEvent{OrgID: org, Source: store.AccessSourceIngress, Decision: store.AccessAllow, PrincipalID: "usr_1", Status: 200}
}

func TestRecordFillsIDAndTimeAndSkipsOrgless(t *testing.T) {
	c := &clock{now: time.Unix(1000, 0)}
	l := New(&fakeSink{}, Options{Now: c.Now, Log: quiet()})
	e := allow("org_1")
	l.Record(e)
	l.Record(&store.AccessEvent{Decision: store.AccessAllow})
	l.Record(nil)
	var nilLogger *Logger
	nilLogger.Record(allow("org_1"))
	if e.ID == "" || !e.At.Equal(c.now) || len(l.queue) != 1 {
		t.Fatalf("event = %+v, queued %d", e, len(l.queue))
	}
}

func TestQueueFullDropsAndCounts(t *testing.T) {
	l := New(&fakeSink{}, Options{QueueSize: 2, Log: quiet()})
	for range 5 {
		l.Record(allow("org_1"))
	}
	if l.Dropped() != 3 || len(l.queue) != 2 {
		t.Fatalf("dropped = %d queued = %d", l.Dropped(), len(l.queue))
	}
}

func TestAuthFailureLimiter(t *testing.T) {
	c := &clock{now: time.Unix(1000, 0)}
	l := New(&fakeSink{}, Options{Now: c.Now, Log: quiet(), FailureRate: 1, FailureBurst: 3, PerKeyWindow: 5 * time.Second, QueueSize: 100})
	fail := func(ip, reason string) *store.AccessEvent {
		return &store.AccessEvent{OrgID: "org_1", Decision: store.AccessDeny, ClientIP: ip, Reason: reason, Status: 401}
	}
	l.Record(fail("192.0.2.1", "unauthenticated"))
	l.Record(fail("192.0.2.1", "unauthenticated"))
	l.Record(fail("192.0.2.1", "bad_token"))
	l.Record(fail("192.0.2.2", "unauthenticated"))
	l.Record(fail("192.0.2.3", "unauthenticated"))
	if len(l.queue) != 3 || l.Suppressed() != 2 {
		t.Fatalf("queued %d suppressed %d", len(l.queue), l.Suppressed())
	}
	l.Record(allow("org_1"))
	l.Record(&store.AccessEvent{OrgID: "org_1", Decision: store.AccessDeny, PrincipalID: "usr_1", Status: 403})
	if len(l.queue) != 5 {
		t.Fatalf("allow and authenticated deny must bypass limiter, queued %d", len(l.queue))
	}
	c.advance(6 * time.Second)
	l.Record(fail("192.0.2.1", "unauthenticated"))
	if len(l.queue) != 6 {
		t.Fatalf("key window and bucket refill, queued %d", len(l.queue))
	}
}

func TestFailureKeyFloodDoesNotResetDedup(t *testing.T) {
	c := &clock{now: time.Unix(1000, 0)}
	l := New(&fakeSink{}, Options{Now: c.Now, Log: quiet(), FailureRate: 1e9, FailureBurst: 1e9, PerKeyWindow: time.Minute})
	if !l.admitFailure("ip:192.0.2.1|unauthenticated") {
		t.Fatal("first failure must be admitted")
	}
	for i := range 2 * maxFailureKeys {
		l.admitFailure("ip:flood-" + strconv.Itoa(i) + "|unauthenticated")
		c.advance(time.Millisecond)
	}
	if l.admitFailure("ip:192.0.2.1|unauthenticated") {
		t.Fatal("key flood reset per-key dedup window")
	}
	if len(l.lastSeen) > maxFailureKeys {
		t.Fatalf("lastSeen grew to %d", len(l.lastSeen))
	}
}

func TestRunBatchesFlushesAndDrainsOnStop(t *testing.T) {
	sink := &fakeSink{}
	l := New(sink, Options{BatchSize: 2, FlushInterval: time.Hour, Log: quiet()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	for range 5 {
		l.Record(allow("org_1"))
	}
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if sink.count() != 5 {
		t.Fatalf("written %d, want 5", sink.count())
	}
}

func TestRunPrunesWithRetention(t *testing.T) {
	sink := &fakeSink{}
	c := &clock{now: time.Unix(100000, 0)}
	l := New(sink, Options{Now: c.Now, Retention: time.Hour, Log: quiet()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.Run(ctx)
	if len(sink.pruned) != 1 || !sink.pruned[0].Equal(c.now.Add(-time.Hour)) {
		t.Fatalf("pruned = %v", sink.pruned)
	}
}

func TestWriteFailureIsCounted(t *testing.T) {
	sink := &fakeSink{fail: errors.New("down")}
	l := New(sink, Options{Log: quiet()})
	l.Record(allow("org_1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.Run(ctx)
	if l.WriteFailures() != 1 {
		t.Fatalf("write failures = %d", l.WriteFailures())
	}
}
