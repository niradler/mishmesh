package flows

import (
	"context"
	"errors"
	"fmt"
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
	flushers []func(string)
}

func (b *flushBus) Subscribe(func(identity.Revocation)) func() { return func() {} }

func (b *flushBus) OnFlush(fn func(string)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushers = append(b.flushers, fn)
	return func() {}
}

func (b *flushBus) flush() {
	b.flushOrg("")
}

func (b *flushBus) flushOrg(orgID string) {
	b.mu.Lock()
	fns := append([]func(string){}, b.flushers...)
	b.mu.Unlock()
	for _, fn := range fns {
		fn(orgID)
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

func TestOrgFlushOnlyReevaluatesThatOrg(t *testing.T) {
	bus := &flushBus{}
	tr := flushTracker(bus)
	aNear, aFar := net.Pipe()
	defer aFar.Close()
	bNear, bFar := net.Pipe()
	defer bNear.Close()
	defer bFar.Close()
	deny := func(context.Context) error { return errors.New("policy now denies") }
	_, untrackA := tr.Track("org_a", "", aNear, deny)
	defer untrackA()
	var otherChecks atomic.Int32
	_, untrackB := tr.Track("org_b", "", bNear, func(ctx context.Context) error {
		otherChecks.Add(1)
		return deny(ctx)
	})
	defer untrackB()

	bus.flushOrg("org_a")
	tr.WaitReevaluated()

	if !closedWithin(aFar, time.Second) {
		t.Fatal("flow in the flushed org is still open")
	}
	if otherChecks.Load() != 0 {
		t.Fatal("a flush for one org re-evaluated another org's flow")
	}
	go func() { _, _ = bNear.Read(make([]byte, 1)) }()
	if _, err := bFar.Write([]byte("x")); err != nil {
		t.Fatalf("flow in another org was closed: %v", err)
	}
}

func TestTransientRecheckErrors(t *testing.T) {
	cases := []struct {
		name      string
		failures  int32
		wantOpen  bool
		wantCalls int32
	}{
		{"recovers within the retry budget", 1, true, 2},
		{"persistent unavailability fails closed", recheckAttempts, false, recheckAttempts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := &flushBus{}
			tr := flushTracker(bus)
			tr.backoff = time.Millisecond
			near, far := net.Pipe()
			defer near.Close()
			defer far.Close()
			var calls atomic.Int32
			_, untrack := tr.Track("org_a", "", near, func(context.Context) error {
				if calls.Add(1) <= tc.failures {
					return fmt.Errorf("store timeout: %w", ErrTransient)
				}
				return nil
			})
			defer untrack()
			bus.flush()
			tr.WaitReevaluated()
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("recheck ran %d times, want %d", got, tc.wantCalls)
			}
			if closed := closedWithin(far, 100*time.Millisecond); closed == tc.wantOpen {
				t.Fatalf("flow closed = %v, want open = %v", closed, tc.wantOpen)
			}
		})
	}
}

func TestConfirmRechecksFlowsTrackedAcrossAFlush(t *testing.T) {
	bus := &flushBus{}
	tr := flushTracker(bus)
	var calls atomic.Int32
	deny := func(context.Context) error {
		calls.Add(1)
		return errors.New("policy now denies")
	}

	fresh := tr.Epoch()
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	f, untrack := tr.Track("org_a", "", near, deny)
	defer untrack()
	bus.flushOrg("org_b")
	tr.WaitReevaluated()
	if !tr.Confirm(f, fresh) || calls.Load() != 0 {
		t.Fatalf("flow re-checked after an unrelated org flush (calls %d)", calls.Load())
	}

	decided := tr.Epoch()
	bus.flushOrg("org_a")
	tr.WaitReevaluated()
	calls.Store(0)
	staleNear, staleFar := net.Pipe()
	defer staleFar.Close()
	stale, untrackStale := tr.Track("org_a", "", staleNear, deny)
	defer untrackStale()
	if tr.Confirm(stale, decided) {
		t.Fatal("flow decided before a policy flush was confirmed without a re-check")
	}
	if calls.Load() != 1 || !closedWithin(staleFar, time.Second) {
		t.Fatalf("stale flow re-checked %d times and left open", calls.Load())
	}
}
