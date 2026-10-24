package flows

import (
	"log/slog"
	"net"
	"slices"
	"sync"

	"github.com/mishmesh/mishmesh/internal/identity"
)

type Revocations interface {
	Subscribe(fn func(identity.Revocation)) func()
}

type Flow struct {
	orgID     string
	sessionID string
	conns     []net.Conn
	revoked   bool
}

type Tracker struct {
	log   *slog.Logger
	label string

	mu    sync.Mutex
	flows map[*Flow]struct{}
}

func NewTracker(log *slog.Logger, label string, revocations Revocations) *Tracker {
	t := &Tracker{log: log, label: label, flows: make(map[*Flow]struct{})}
	if revocations != nil {
		revocations.Subscribe(t.Revoke)
	}
	return t
}

func (t *Tracker) Track(orgID, sessionID string, c net.Conn) (*Flow, func()) {
	f := &Flow{orgID: orgID, sessionID: sessionID, conns: []net.Conn{c}}
	t.mu.Lock()
	t.flows[f] = struct{}{}
	t.mu.Unlock()
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
