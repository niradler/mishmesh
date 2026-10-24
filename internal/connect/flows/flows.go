package flows

import (
	"context"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
)

type Revocations interface {
	Subscribe(fn func(identity.Revocation)) func()
}

type SessionChecker interface {
	SessionActive(ctx context.Context, id string) (bool, error)
}

const (
	defaultSweepEvery = 30 * time.Second
	sweepCheckTimeout = 2 * time.Second
)

type Flow struct {
	orgID     string
	sessionID string
	conns     []net.Conn
	revoked   bool
}

type Tracker struct {
	log        *slog.Logger
	label      string
	checker    SessionChecker
	sweepEvery time.Duration

	mu       sync.Mutex
	flows    map[*Flow]struct{}
	sweeping bool
}

func NewTracker(log *slog.Logger, label string, revocations Revocations) *Tracker {
	t := &Tracker{log: log, label: label, sweepEvery: defaultSweepEvery, flows: make(map[*Flow]struct{})}
	if revocations != nil {
		revocations.Subscribe(t.Revoke)
		t.checker, _ = revocations.(SessionChecker)
	}
	return t
}

func (t *Tracker) Track(orgID, sessionID string, c net.Conn) (*Flow, func()) {
	f := &Flow{orgID: orgID, sessionID: sessionID, conns: []net.Conn{c}}
	t.mu.Lock()
	t.flows[f] = struct{}{}
	startSweep := t.checker != nil && !t.sweeping
	t.sweeping = t.sweeping || startSweep
	t.mu.Unlock()
	if startSweep {
		go t.sweepLoop()
	}
	return f, func() {
		t.mu.Lock()
		delete(t.flows, f)
		t.mu.Unlock()
	}
}

func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.flows)
}

func (t *Tracker) Attach(f *Flow, c net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if f.revoked {
		return false
	}
	f.conns = append(f.conns, c)
	return true
}

func (t *Tracker) Revoke(rev identity.Revocation) {
	if len(rev.SessionIDs) == 0 {
		return
	}
	var doomed []net.Conn
	hits := 0
	t.mu.Lock()
	for f := range t.flows {
		if f.orgID == rev.OrgID && slices.Contains(rev.SessionIDs, f.sessionID) {
			f.revoked = true
			doomed = append(doomed, f.conns...)
			hits++
		}
	}
	t.mu.Unlock()
	for _, c := range doomed {
		_ = c.Close()
	}
	if hits > 0 {
		t.log.Info("closed "+t.label+" flows for revoked sessions", "org_id", rev.OrgID, "flows", hits)
	}
}

type sessionRef struct {
	orgID     string
	sessionID string
}

func (t *Tracker) sweepLoop() {
	ticker := time.NewTicker(t.sweepEvery)
	defer ticker.Stop()
	for range ticker.C {
		refs, ok := t.liveSessions()
		if !ok {
			return
		}
		for _, ref := range refs {
			t.sweepSession(ref)
		}
	}
}

func (t *Tracker) liveSessions() ([]sessionRef, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.flows) == 0 {
		t.sweeping = false
		return nil, false
	}
	seen := make(map[sessionRef]struct{}, len(t.flows))
	refs := make([]sessionRef, 0, len(t.flows))
	for f := range t.flows {
		ref := sessionRef{orgID: f.orgID, sessionID: f.sessionID}
		if _, dup := seen[ref]; dup || f.revoked {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs, true
}

func (t *Tracker) sweepSession(ref sessionRef) {
	ctx, cancel := context.WithTimeout(context.Background(), sweepCheckTimeout)
	defer cancel()
	active, err := t.checker.SessionActive(ctx, ref.sessionID)
	if err != nil {
		t.log.Warn(t.label+" flow session check failed", "org_id", ref.orgID, "err", err)
		return
	}
	if !active {
		t.Revoke(identity.Revocation{OrgID: ref.orgID, SessionIDs: []string{ref.sessionID}})
	}
}
