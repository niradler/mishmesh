package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const decisionTimeout = 5 * time.Second

var errDecisionLost = errors.New("decision lost: tunnel session changed")

type DeniedError struct {
	Reason string
}

func (e *DeniedError) Error() string {
	if e.Reason == "" {
		return "connect denied by policy"
	}
	return "connect denied by policy: " + e.Reason
}

type decisionKey struct {
	action  string
	connect string
	kind    string
}

type cachedDecision struct {
	allowed bool
	reason  string
	expires time.Time
}

type remoteDecider struct {
	now func() time.Time

	mu      sync.Mutex
	gen     uint64
	epoch   uint64
	ctrl    *tunnel.Control
	nextID  uint64
	pending map[uint64]chan tunnel.DecisionAnswer
	cache   map[decisionKey]cachedDecision
}

func newRemoteDecider(now func() time.Time) *remoteDecider {
	return &remoteDecider{now: now, pending: map[uint64]chan tunnel.DecisionAnswer{}, cache: map[decisionKey]cachedDecision{}}
}

func (d *remoteDecider) attach(ctrl *tunnel.Control) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resetLocked()
	d.gen++
	d.ctrl = ctrl
	return d.gen
}

func (d *remoteDecider) detach(gen uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	d.resetLocked()
	d.ctrl = nil
}

func (d *remoteDecider) flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.epoch++
	clear(d.cache)
}

func (d *remoteDecider) resetLocked() {
	d.epoch++
	clear(d.cache)
	for id, ch := range d.pending {
		close(ch)
		delete(d.pending, id)
	}
}

func (d *remoteDecider) deliver(gen uint64, answer tunnel.DecisionAnswer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	ch, ok := d.pending[answer.ID]
	if !ok {
		return
	}
	delete(d.pending, answer.ID)
	ch <- answer
}

func (d *remoteDecider) check(ctx context.Context, connect, kind string) error {
	key := decisionKey{action: tunnel.DecisionActionConnect, connect: connect, kind: kind}
	d.mu.Lock()
	if c, ok := d.cache[key]; ok && d.now().Before(c.expires) {
		d.mu.Unlock()
		return verdict(c.allowed, c.reason)
	}
	if d.ctrl == nil {
		d.mu.Unlock()
		return errNotConnected
	}
	d.nextID++
	id, gen, epoch, ctrl := d.nextID, d.gen, d.epoch, d.ctrl
	ch := make(chan tunnel.DecisionAnswer, 1)
	d.pending[id] = ch
	d.mu.Unlock()

	query := tunnel.DecisionQuery{ID: id, Action: key.action, Connect: connect, Kind: kind}
	if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgDecisionRequest, Decision: &query}); err != nil {
		d.forget(gen, id)
		return fmt.Errorf("send decision request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	select {
	case answer, ok := <-ch:
		if !ok {
			return errDecisionLost
		}
		d.remember(gen, epoch, key, answer)
		return verdict(answer.Allowed, answer.Reason)
	case <-ctx.Done():
		d.forget(gen, id)
		return fmt.Errorf("decision request: %w", ctx.Err())
	}
}

func (d *remoteDecider) forget(gen, id uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen == gen {
		delete(d.pending, id)
	}
}

func (d *remoteDecider) remember(gen, epoch uint64, key decisionKey, answer tunnel.DecisionAnswer) {
	ttl := min(answer.TTLSeconds, tunnel.MaxDecisionTTLSeconds)
	if ttl <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen || d.epoch != epoch {
		return
	}
	d.cache[key] = cachedDecision{allowed: answer.Allowed, reason: answer.Reason, expires: d.now().Add(time.Duration(ttl) * time.Second)}
}

func verdict(allowed bool, reason string) error {
	if allowed {
		return nil
	}
	return &DeniedError{Reason: reason}
}
