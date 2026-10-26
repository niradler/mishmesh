package controlplane

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type auditRecorder struct {
	store.DataStore
	mu     sync.Mutex
	events []*store.AuditEvent
	ctxErr error
}

func (s *auditRecorder) AppendAudit(ctx context.Context, ev *store.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctxErr = ctx.Err()
	if s.ctxErr != nil {
		return s.ctxErr
	}
	s.events = append(s.events, ev)
	return nil
}

func newAuditRecorderAPI(t *testing.T) (*API, *auditRecorder) {
	t.Helper()
	sq, err := sqlite.Open(t.TempDir() + "/audit.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sq.Close() })
	rec := &auditRecorder{DataStore: sq}
	return New(rec, memory.NewConnStore(), "admin-secret", slog.New(slog.NewTextHandler(io.Discard, nil))), rec
}

func TestAuditSurvivesRequestCancellation(t *testing.T) {
	api, rec := newAuditRecorderAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/x", nil).WithContext(ctx)
	cancel()
	api.audit(r, "agent.revoke", "ag_1", "")
	if len(rec.events) != 1 {
		t.Fatalf("audit event dropped when the request context was cancelled (ctx err %v)", rec.ctxErr)
	}
}

func TestAuditAsSurvivesRequestCancellation(t *testing.T) {
	api, rec := newAuditRecorderAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/x", nil).WithContext(ctx)
	cancel()
	api.auditAs(r, "org_1", "machine:x", "token.exchange", "t", "")
	if len(rec.events) != 1 {
		t.Fatalf("auditAs event dropped when the request context was cancelled (ctx err %v)", rec.ctxErr)
	}
}

func TestAuditUsesInjectedClock(t *testing.T) {
	api, rec := newAuditRecorderAPI(t)
	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	api.now = func() time.Time { return fixed }
	api.audit(httptest.NewRequest(http.MethodPost, "/x", nil), "agent.revoke", "ag_1", "")
	if len(rec.events) != 1 || !rec.events[0].CreatedAt.Equal(fixed) {
		t.Fatalf("audit timestamp ignored the injected clock: %+v", rec.events)
	}
}
