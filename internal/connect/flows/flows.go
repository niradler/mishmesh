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

type Flushes interface {
	OnFlush(fn func()) func()
}

type Recheck func(ctx context.Context) error

const (
	defaultSweepEvery = 30 * time.Second
	sweepCheckTimeout = 2 * time.Second
	recheckTimeout    = 5 * time.Second
	recheckParallel   = 8
)

type Flow struct {
	orgID     string
	sessionID string
	recheck   Recheck
	conns     []net.Conn
	revoked   bool
}

type Tracker struct {
	log        *slog.Logger
	label      string
	checker    SessionChecker
	sweepEvery time.Duration

	mu           sync.Mutex
	flows        map[*Flow]struct{}
	sweeping     bool
	reevaluating bool
	pending      bool
	idle         *sync.Cond
}

func NewTracker(log *slog.Logger, label string, revocations Revocations) *Tracker {
	t := &Tracker{log: log, label: label, sweepEvery: defaultSweepEvery, flows: make(map[*Flow]struct{})}
	t.idle = sync.NewCond(&t.mu)
	if revocations != nil {
		revocations.Subscribe(t.Revoke)
		t.checker, _ = revocations.(SessionChecker)
		if flushes, ok := revocations.(Flushes); ok {
			flushes.OnFlush(t.Reevaluate)
		}
	}
	return t
}

func (t *Tracker) Track(orgID, sessionID string, c net.Conn, recheck Recheck) (*Flow, func()) {
	f := &Flow{orgID: orgID, sessionID: sessionID, recheck: recheck, conns: []net.Conn{c}}
	t.mu.Lock()
	t.flows[f] = struct{}{}
	startSweep := t.checker != nil && sessionID != "" && !t.sweeping
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
		if f.sessionID != "" && f.orgID == rev.OrgID && slices.Contains(rev.SessionIDs, f.sessionID) {
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
		if _, dup := seen[ref]; dup || f.revoked || f.sessionID == "" {
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

func (t *Tracker) Reevaluate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reevaluating {
		t.pending = true
		return
	}
	t.reevaluating = true
	go t.reevaluateLoop()
}

func (t *Tracker) WaitReevaluated() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for t.reevaluating {
		t.idle.Wait()
	}
}

func (t *Tracker) reevaluateLoop() {
	for {
		t.reevaluateOnce()
		t.mu.Lock()
		if !t.pending {
			t.reevaluating = false
			t.idle.Broadcast()
			t.mu.Unlock()
			return
		}
		t.pending = false
		t.mu.Unlock()
	}
}

func (t *Tracker) reevaluateOnce() {
	t.mu.Lock()
	checks := make([]*Flow, 0, len(t.flows))
	for f := range t.flows {
		if !f.revoked && f.recheck != nil {
			checks = append(checks, f)
		}
	}
	t.mu.Unlock()
	sem := make(chan struct{}, recheckParallel)
	var wg sync.WaitGroup
	for _, f := range checks {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			t.recheckFlow(f)
		})
	}
	wg.Wait()
}

func (t *Tracker) recheckFlow(f *Flow) {
	ctx, cancel := context.WithTimeout(context.Background(), recheckTimeout)
	defer cancel()
	err := f.recheck(ctx)
	if err == nil {
		return
	}
	t.mu.Lock()
	f.revoked = true
	doomed := slices.Clone(f.conns)
	t.mu.Unlock()
	for _, c := range doomed {
		_ = c.Close()
	}
	t.log.Info("closed "+t.label+" flow after policy re-evaluation", "org_id", f.orgID, "err", err)
}
