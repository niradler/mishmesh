package flows

import (
	"context"
	"errors"
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
	OnFlush(fn func(orgID string)) func()
}

type Recheck func(ctx context.Context) error

var ErrTransient = errors.New("flows: re-evaluation unavailable")

const (
	defaultSweepEvery = 30 * time.Second
	sweepCheckTimeout = 2 * time.Second
	recheckTimeout    = 5 * time.Second
	recheckParallel   = 8
	recheckAttempts   = 3
	defaultBackoff    = time.Second
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
	backoff    time.Duration

	mu           sync.Mutex
	flows        map[*Flow]struct{}
	sweeping     bool
	reevaluating bool
	pending      map[string]struct{}
	epoch        uint64
	flushedAt    map[string]uint64
	idle         *sync.Cond

	stop      chan struct{}
	closeOnce sync.Once
	unsubs    []func()
}

func NewTracker(log *slog.Logger, label string, revocations Revocations) *Tracker {
	t := &Tracker{
		log: log, label: label, sweepEvery: defaultSweepEvery, backoff: defaultBackoff,
		flows: make(map[*Flow]struct{}), pending: make(map[string]struct{}), flushedAt: make(map[string]uint64),
		stop: make(chan struct{}),
	}
	t.idle = sync.NewCond(&t.mu)
	if revocations != nil {
		t.unsubs = append(t.unsubs, revocations.Subscribe(t.Revoke))
		t.checker, _ = revocations.(SessionChecker)
		if flushes, ok := revocations.(Flushes); ok {
			t.unsubs = append(t.unsubs, flushes.OnFlush(t.Reevaluate))
		}
	}
	return t
}

func (t *Tracker) Close() {
	t.closeOnce.Do(func() {
		close(t.stop)
		for _, unsub := range t.unsubs {
			if unsub != nil {
				unsub()
			}
		}
	})
}

func (t *Tracker) stopped() bool {
	select {
	case <-t.stop:
		return true
	default:
		return false
	}
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
	for {
		select {
		case <-ticker.C:
		case <-t.stop:
			t.mu.Lock()
			t.sweeping = false
			t.mu.Unlock()
			return
		}
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

func (t *Tracker) Reevaluate(orgID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.epoch++
	t.flushedAt[orgID] = t.epoch
	if t.reevaluating {
		t.pending[orgID] = struct{}{}
		return
	}
	t.reevaluating = true
	go t.reevaluateLoop(map[string]struct{}{orgID: {}})
}

func (t *Tracker) Epoch() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.epoch
}

func (t *Tracker) Confirm(f *Flow, epoch uint64) bool {
	t.mu.Lock()
	stale := t.flushedAt[f.orgID] > epoch || t.flushedAt[""] > epoch
	t.mu.Unlock()
	if stale && f.recheck != nil {
		t.recheckFlow(f)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !f.revoked
}

func (t *Tracker) WaitReevaluated() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for t.reevaluating {
		t.idle.Wait()
	}
}

func (t *Tracker) reevaluateLoop(orgs map[string]struct{}) {
	for {
		t.reevaluateOnce(orgs)
		t.mu.Lock()
		if len(t.pending) == 0 {
			t.reevaluating = false
			t.idle.Broadcast()
			t.mu.Unlock()
			return
		}
		orgs = t.pending
		t.pending = make(map[string]struct{})
		t.mu.Unlock()
	}
}

func (t *Tracker) reevaluateOnce(orgs map[string]struct{}) {
	_, everyOrg := orgs[""]
	t.mu.Lock()
	checks := make([]*Flow, 0, len(t.flows))
	for f := range t.flows {
		if _, hit := orgs[f.orgID]; (everyOrg || hit) && !f.revoked && f.recheck != nil {
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
	err := t.recheckWithRetry(f)
	if err == nil || t.stopped() {
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

func (t *Tracker) recheckWithRetry(f *Flow) error {
	var err error
	for attempt := range recheckAttempts {
		if attempt > 0 {
			wait := time.NewTimer(t.backoff << (attempt - 1))
			select {
			case <-wait.C:
			case <-t.stop:
				wait.Stop()
				return err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), recheckTimeout)
		err = f.recheck(ctx)
		cancel()
		if err == nil || !errors.Is(err, ErrTransient) {
			return err
		}
		t.log.Warn(t.label+" flow re-evaluation unavailable; retrying", "org_id", f.orgID, "attempt", attempt+1, "err", err)
	}
	return err
}
