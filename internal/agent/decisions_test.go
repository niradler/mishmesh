package agent

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type decisionHarness struct {
	dec     *remoteDecider
	gen     uint64
	gateway *tunnel.Control
	clock   *fakeClock
}

func newDecisionHarness(t *testing.T) *decisionHarness {
	t.Helper()
	a, b := net.Pipe()
	client, err := tunnel.Client(a)
	if err != nil {
		t.Fatal(err)
	}
	server, err := tunnel.Server(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	accepted := make(chan *tunnel.Control, 1)
	go func() {
		ctrl, err := server.AcceptControl()
		if err == nil {
			accepted <- ctrl
		}
	}()
	ctrl, err := client.OpenControl()
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	h := &decisionHarness{dec: newRemoteDecider(clock.Now), clock: clock}
	h.gen = h.dec.attach(ctrl)
	if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgPing}); err != nil {
		t.Fatal(err)
	}
	select {
	case h.gateway = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("control stream not accepted")
	}
	if _, err := h.gateway.Recv(); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *decisionHarness) answerNext(t *testing.T, allowed bool, reason string, ttl int) tunnel.DecisionQuery {
	t.Helper()
	msg, err := h.gateway.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != tunnel.MsgDecisionRequest || msg.Decision == nil {
		t.Fatalf("got %+v, want decision request", msg)
	}
	h.dec.deliver(h.gen, tunnel.DecisionAnswer{ID: msg.Decision.ID, Allowed: allowed, Reason: reason, TTLSeconds: ttl})
	return *msg.Decision
}

func (h *decisionHarness) checkAsync(connect string) <-chan error {
	out := make(chan error, 1)
	go func() { out <- h.dec.check(context.Background(), connect, "tcp") }()
	return out
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("decision check did not return")
		return nil
	}
}

func TestRemoteDeciderAllowAndDeny(t *testing.T) {
	h := newDecisionHarness(t)
	res := h.checkAsync("db")
	q := h.answerNext(t, true, "", 0)
	if q.Action != tunnel.DecisionActionConnect || q.Connect != "db" || q.Kind != "tcp" {
		t.Fatalf("query = %+v", q)
	}
	if err := waitErr(t, res); err != nil {
		t.Fatalf("allow: err = %v", err)
	}
	res = h.checkAsync("db")
	h.answerNext(t, false, "policy", 0)
	var denied *DeniedError
	if err := waitErr(t, res); !errors.As(err, &denied) || denied.Reason != "policy" {
		t.Fatalf("deny: err = %v", err)
	}
}

func TestRemoteDeciderCachesForTTLAndFlushClears(t *testing.T) {
	h := newDecisionHarness(t)
	res := h.checkAsync("db")
	h.answerNext(t, true, "", 30)
	if err := waitErr(t, res); err != nil {
		t.Fatal(err)
	}
	if err := h.dec.check(context.Background(), "db", "tcp"); err != nil {
		t.Fatalf("cached check: %v", err)
	}
	h.clock.Advance(31 * time.Second)
	res = h.checkAsync("db")
	h.answerNext(t, false, "expired", 30)
	if err := waitErr(t, res); err == nil {
		t.Fatal("expected fresh deny after ttl expiry")
	}
	h.dec.flush()
	res = h.checkAsync("db")
	h.answerNext(t, true, "", 0)
	if err := waitErr(t, res); err != nil {
		t.Fatalf("after flush: %v", err)
	}
}

func TestRemoteDeciderDetachFailsPendingAndClearsCache(t *testing.T) {
	h := newDecisionHarness(t)
	res := h.checkAsync("db")
	if _, err := h.gateway.Recv(); err != nil {
		t.Fatal(err)
	}
	h.dec.detach(h.gen)
	if err := waitErr(t, res); !errors.Is(err, errDecisionLost) {
		t.Fatalf("err = %v, want decision lost", err)
	}
	if err := h.dec.check(context.Background(), "db", "tcp"); !errors.Is(err, errNotConnected) {
		t.Fatalf("detached check err = %v, want not connected", err)
	}
}

func TestRemoteDeciderIgnoresStaleGenerationAnswers(t *testing.T) {
	h := newDecisionHarness(t)
	res := h.checkAsync("db")
	msg, err := h.gateway.Recv()
	if err != nil {
		t.Fatal(err)
	}
	h.dec.deliver(h.gen+1, tunnel.DecisionAnswer{ID: msg.Decision.ID, Allowed: true})
	select {
	case err := <-res:
		t.Fatalf("stale answer accepted: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	h.dec.deliver(h.gen, tunnel.DecisionAnswer{ID: msg.Decision.ID, Allowed: false, Reason: "x"})
	if err := waitErr(t, res); err == nil {
		t.Fatal("expected deny")
	}
}

func TestRemoteDeciderAnswerDuringFlushIsNotCached(t *testing.T) {
	h := newDecisionHarness(t)
	res := h.checkAsync("db")
	msg, err := h.gateway.Recv()
	if err != nil {
		t.Fatal(err)
	}
	h.dec.flush()
	h.dec.deliver(h.gen, tunnel.DecisionAnswer{ID: msg.Decision.ID, Allowed: true, TTLSeconds: 60})
	if err := waitErr(t, res); err != nil {
		t.Fatal(err)
	}
	h.dec.mu.Lock()
	cached := len(h.dec.cache)
	h.dec.mu.Unlock()
	if cached != 0 {
		t.Fatalf("cache size = %d, want 0 for answer that raced a flush", cached)
	}
}
