package flows

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
)

type fakeSessions struct {
	mu       sync.Mutex
	inactive map[string]bool
	failing  bool
	checks   int
}

func (f *fakeSessions) Subscribe(func(identity.Revocation)) func() { return func() {} }

func (f *fakeSessions) SessionActive(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.failing {
		return false, errors.New("store down")
	}
	return !f.inactive[id], nil
}

func (f *fakeSessions) set(fn func(*fakeSessions)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeSessions) checkCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

func sweepingTracker(s *fakeSessions) *Tracker {
	tr := NewTracker(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", s)
	tr.sweepEvery = 10 * time.Millisecond
	return tr
}

func closedWithin(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	_, err := c.Read(make([]byte, 1))
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe)
}

func TestSweepClosesFlowsWhoseSessionEnded(t *testing.T) {
	s := &fakeSessions{inactive: map[string]bool{}}
	tr := sweepingTracker(s)
	goneNear, goneFar := net.Pipe()
	defer goneFar.Close()
	liveNear, liveFar := net.Pipe()
	defer liveNear.Close()
	defer liveFar.Close()
	_, untrackGone := tr.Track("org_a", "ses_gone", goneNear)
	defer untrackGone()
	_, untrackLive := tr.Track("org_a", "ses_live", liveNear)
	defer untrackLive()

	s.set(func(f *fakeSessions) { f.inactive["ses_gone"] = true })
	if !closedWithin(goneFar, 2*time.Second) {
		t.Fatal("flow with an ended session was not closed by the sweep")
	}
	if closedWithin(liveFar, 50*time.Millisecond) {
		t.Fatal("flow with an active session was closed")
	}
}

func TestSweepKeepsFlowsWhenStoreFails(t *testing.T) {
	s := &fakeSessions{failing: true}
	tr := sweepingTracker(s)
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	_, untrack := tr.Track("org_a", "ses_1", near)
	defer untrack()
	deadline := time.Now().Add(2 * time.Second)
	for s.checkCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if closedWithin(far, 20*time.Millisecond) {
		t.Fatal("flow closed on a transient store error")
	}
}

func TestSweepStopsWhenIdle(t *testing.T) {
	s := &fakeSessions{}
	tr := sweepingTracker(s)
	near, far := net.Pipe()
	defer far.Close()
	_, untrack := tr.Track("org_a", "ses_1", near)
	untrack()
	_ = near.Close()
	time.Sleep(50 * time.Millisecond)
	tr.mu.Lock()
	sweeping := tr.sweeping
	tr.mu.Unlock()
	if sweeping {
		t.Fatal("sweeper still running with no flows")
	}
	settled := s.checkCount()
	time.Sleep(50 * time.Millisecond)
	if s.checkCount() != settled {
		t.Fatal("sweeper kept checking sessions after going idle")
	}
}
