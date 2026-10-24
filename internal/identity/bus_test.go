package identity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/token"
)

type loopBus struct {
	mu   sync.Mutex
	subs map[string][]func([]byte)
	fail bool
}

func (b *loopBus) PublishEvent(_ context.Context, topic string, payload []byte) error {
	b.mu.Lock()
	if b.fail {
		b.mu.Unlock()
		return errors.New("bus down")
	}
	fns := append([]func([]byte){}, b.subs[topic]...)
	b.mu.Unlock()
	for _, fn := range fns {
		fn(payload)
	}
	return nil
}

func (b *loopBus) SubscribeEvents(topic string, fn func([]byte)) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = make(map[string][]func([]byte))
	}
	b.subs[topic] = append(b.subs[topic], fn)
	return func() {}, nil
}

func clusterPair(t *testing.T, bus *loopBus) (*Service, *Service) {
	t.Helper()
	keys, err := token.NewKeyring(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sessions := memory.NewSessionStore()
	a, b := New(keys, sessions), New(keys, sessions)
	for _, s := range []*Service{a, b} {
		stop, err := s.AttachBus(bus, discardLog())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(stop)
	}
	return a, b
}

func TestRevocationReachesOtherNodesOnce(t *testing.T) {
	a, b := clusterPair(t, &loopBus{})
	var gotA, gotB []Revocation
	a.Subscribe(func(r Revocation) { gotA = append(gotA, r) })
	b.Subscribe(func(r Revocation) { gotB = append(gotB, r) })
	issued := issue(t, a, "org_1")
	if err := a.RevokeSession(context.Background(), issued.Session.ID); err != nil {
		t.Fatal(err)
	}
	if len(gotA) != 1 || len(gotB) != 1 {
		t.Fatalf("deliveries a=%d b=%d, want 1 each", len(gotA), len(gotB))
	}
	if gotB[0].OrgID != "org_1" || gotB[0].Subject != sa || len(gotB[0].SessionIDs) != 1 || gotB[0].SessionIDs[0] != issued.Session.ID {
		t.Fatalf("remote revocation = %+v", gotB[0])
	}
}

func TestFlushReachesOtherNodesOnce(t *testing.T) {
	a, b := clusterPair(t, &loopBus{})
	var flushA, flushB int
	a.OnFlush(func() { flushA++ })
	b.OnFlush(func() { flushB++ })
	a.FlushAll()
	if flushA != 1 || flushB != 1 {
		t.Fatalf("flushes a=%d b=%d, want 1 each", flushA, flushB)
	}
}

func TestBusFailureStillRevokesLocally(t *testing.T) {
	a, _ := clusterPair(t, &loopBus{fail: true})
	var got int
	a.Subscribe(func(Revocation) { got++ })
	issued := issue(t, a, "org_1")
	if err := a.RevokeSession(context.Background(), issued.Session.ID); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("local deliveries = %d, want 1", got)
	}
}

func TestRemoteMessagesValidated(t *testing.T) {
	bus := &loopBus{}
	_, b := clusterPair(t, bus)
	var got int
	b.Subscribe(func(Revocation) { got++ })
	for _, payload := range []string{`not json`, `{"org":"org_1","sessions":["s"]}`, `{"origin":"x","sessions":["s"]}`, `{"origin":"x","org":"org_1"}`} {
		_ = bus.PublishEvent(context.Background(), revokeTopic, []byte(payload))
	}
	if got != 0 {
		t.Fatalf("invalid remote messages delivered %d revocations", got)
	}
}

func TestSessionActive(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	issued := issue(t, s, "org_1")
	if ok, err := s.SessionActive(ctx, issued.Session.ID); err != nil || !ok {
		t.Fatalf("fresh session active = %v, %v", ok, err)
	}
	s.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if ok, err := s.SessionActive(ctx, issued.Session.ID); err != nil || ok {
		t.Fatalf("expired session active = %v, %v", ok, err)
	}
	if ok, err := s.SessionActive(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing session active = %v, %v", ok, err)
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
