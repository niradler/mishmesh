package flows

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
)

type unsubscribeBus struct {
	revokeUnsubs atomic.Int32
	flushUnsubs  atomic.Int32
}

func (b *unsubscribeBus) Subscribe(func(identity.Revocation)) func() {
	return func() { b.revokeUnsubs.Add(1) }
}

func (b *unsubscribeBus) OnFlush(func(string)) func() {
	return func() { b.flushUnsubs.Add(1) }
}

func TestCloseUnsubscribesFromBuses(t *testing.T) {
	bus := &unsubscribeBus{}
	tr := flushTracker(&flushBus{})
	tr.Close()
	tr = NewTracker(quietLogger(), "test", bus)
	tr.Close()
	tr.Close()
	if bus.revokeUnsubs.Load() != 1 || bus.flushUnsubs.Load() != 1 {
		t.Fatalf("unsubscribes = revoke %d flush %d, want 1 each", bus.revokeUnsubs.Load(), bus.flushUnsubs.Load())
	}
}

func TestCloseInterruptsRetryBackoffWithoutRevoking(t *testing.T) {
	tr := quietTracker()
	tr.backoff = time.Hour
	near, far := net.Pipe()
	defer far.Close()
	attempts := make(chan struct{}, recheckAttempts)
	transient := func(context.Context) error {
		attempts <- struct{}{}
		return ErrTransient
	}
	f, untrack := tr.Track("org_a", "", near, transient)
	defer untrack()

	done := make(chan struct{})
	go func() {
		tr.recheckFlow(f)
		close(done)
	}()
	<-attempts
	tr.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("recheck still sleeping in backoff after Close")
	}
	if !tr.Confirm(f, tr.Epoch()) {
		t.Fatal("flow was revoked because the tracker shut down")
	}
}
