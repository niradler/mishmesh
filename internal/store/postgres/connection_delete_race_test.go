package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldRejectDeleteAfterConcurrentAuthorizedUpdate(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	org := store.NewID("revieworg")
	if err := s.CreateOrg(ctx, &store.Org{ID: org, Name: org, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	row := &store.Connection{ID: store.NewID("reviewconn"), OrgID: org, Type: "third", Name: "initial", Config: []byte(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateConnection(ctx, row); err != nil {
		t.Fatal(err)
	}
	expected, err := s.GetConnection(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Commit()
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE endpoint_aliases IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- s.DeleteConnectionIfCurrent(ctx, expected) }()
	for {
		var waiting bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'SELECT (SELECT COUNT(*) FROM service_connections%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("delete exited before blocking: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	proposal := *expected
	proposal.Name = "owner-protected"
	proposal.Config = []byte(`{"command":["owner-command"]}`)
	proposal.UpdatedAt = time.Now()
	if err := s.UpdateConnectionIfCurrent(ctx, &proposal, expected); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	err = <-result
	if !errors.Is(err, store.ErrConflict) {
		_, lookup := s.GetConnection(ctx, row.ID)
		t.Fatalf("stale delete accepted after owner update: delete=%v, row lookup=%v", err, lookup)
	}
}
