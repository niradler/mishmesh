package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestAccessLogGainsExitAgentColumnOnReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "access.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE access_log DROP COLUMN exit_agent_id`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ev := &store.AccessEvent{ID: store.NewID("acc"), OrgID: "org_1", At: time.Now().UTC(), Source: store.AccessSourceEgress, Decision: store.AccessAllow, AgentID: "ag_src", ExitAgentID: "ag_exit"}
	if err := s.AppendAccessEvents(ctx, []*store.AccessEvent{ev}); err != nil {
		t.Fatalf("append after migration: %v", err)
	}
	got, err := s.ListAccessEvents(ctx, "org_1", store.AccessLogQuery{})
	if err != nil || len(got) != 1 || got[0].ExitAgentID != "ag_exit" {
		t.Fatalf("events = %+v, %v", got, err)
	}
}
