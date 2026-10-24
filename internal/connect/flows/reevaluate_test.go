package flows

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
)

type flushBus struct {
	mu       sync.Mutex
	flushers []func()
}

func (b *flushBus) Subscribe(func(identity.Revocation)) func() { return func() {} }

func (b *flushBus) OnFlush(fn func()) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushers = append(b.flushers, fn)
	return func() {}
}

func (b *flushBus) flush() {
	b.mu.Lock()
	fns := append([]func(){}, b.flushers...)
	b.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

type switchable struct{ deny atomic.Bool }

func (s *switchable) recheck(context.Context) error {
	if s.deny.Load() {
		return errors.New("policy now denies")
	}
	return nil
}

func flushTracker(b *flushBus) *Tracker {
	return NewTracker(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", b)
}

func TestFlushReevaluatesAndClosesDeniedFlows(t *testing.T) {
	bus := &flushBus{}
	tr := flushTracker(bus)
	deniedNear, deniedFar := net.Pipe()
	defer deniedFar.Close()
	keptNear, keptFar := net.Pipe()
	defer keptNear.Close()
	defer keptFar.Close()
	denied, kept := &switchable{}, &switchable{}
	_, untrackDenied := tr.Track("org_a", "", deniedNear, denied.recheck)
	defer untrackDenied()
	f, untrackKept := tr.Track("org_a", "ses_1", keptNear, kept.recheck)
	defer untrackKept()

	denied.deny.Store(true)
	bus.flush()
	tr.WaitReevaluated()

	if !closedWithin(deniedFar, time.Second) {
		t.Fatal("flow denied by the new policy is still open")
	}
	go func() { _, _ = keptNear.Read(make([]byte, 1)) }()
	if _, err := keptFar.Write([]byte("x")); err != nil {
		t.Fatalf("flow still allowed was closed: %v", err)
	}
	extraNear, extraFar := net.Pipe()
	defer extraNear.Close()
	defer extraFar.Close()
	if !tr.Attach(f, extraNear) {
		t.Fatal("allowed flow refused attach after re-evaluation")
	}
}

func TestRecheckErrorFailsClosed(t *testing.T) {
	bus := &flushBus{}
	tr := flushTracker(bus)
	near, far := net.Pipe()
	defer far.Close()
	f, untrack := tr.Track("org_a", "ses_1", near, func(context.Context) error {
		return errors.New("decider unavailable")
	})
	defer untrack()
	bus.flush()
	tr.WaitReevaluated()
	if !closedWithin(far, time.Second) {
		t.Fatal("flow survived a failed re-evaluation")
	}
	extraNear, extraFar := net.Pipe()
	defer extraNear.Close()
	defer extraFar.Close()
	if tr.Attach(f, extraNear) {
		t.Fatal("attach succeeded on a flow closed by re-evaluation")
	}
}

func TestFlowWithoutRecheckIsLeftAlone(t *testing.T) {
	bus := &flushBus{}
	tr := flushTracker(bus)
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	_, untrack := tr.Track("org_a", "ses_1", near, nil)
	defer untrack()
	bus.flush()
	tr.WaitReevaluated()
	go func() { _, _ = near.Read(make([]byte, 1)) }()
	if _, err := far.Write([]byte("x")); err != nil {
		t.Fatalf("flow without a recheck was closed: %v", err)
	}
}

func TestConcurrentFlushesCoalesceAndRerun(t *testing.T) {
	bus := &flushBus{}
	tr := flushTracker(bus)
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	release := make(chan struct{})
	var calls atomic.Int32
	_, untrack := tr.Track("org_a", "ses_1", near, func(context.Context) error {
		if calls.Add(1) == 1 {
			<-release
		}
		return nil
	})
	defer untrack()
	bus.flush()
	for range 5 {
		bus.flush()
	}
	close(release)
	tr.WaitReevaluated()
	if got := calls.Load(); got != 2 {
		t.Fatalf("recheck ran %d times, want 2 (one in flight plus one coalesced rerun)", got)
	}
}

func TestEmptySessionIsNeverRevokedOrSwept(t *testing.T) {
	s := &fakeSessions{inactive: map[string]bool{"": true}}
	tr := sweepingTracker(s)
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	_, untrack := tr.Track("org_a", "", near, nil)
	defer untrack()
	tr.Revoke(identity.Revocation{OrgID: "org_a", SessionIDs: []string{""}})
	time.Sleep(50 * time.Millisecond)
	if s.checkCount() != 0 {
		t.Fatalf("session sweep checked an agent-only flow %d times", s.checkCount())
	}
	go func() { _, _ = near.Read(make([]byte, 1)) }()
	if _, err := far.Write([]byte("x")); err != nil {
		t.Fatalf("agent-only flow was closed: %v", err)
	}
}
