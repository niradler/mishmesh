package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPragmasSurviveNewConnections(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.db.SetMaxIdleConns(0)
	ctx := context.Background()
	cases := []struct {
		pragma string
		want   string
	}{
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"journal_mode", "wal"},
		{"synchronous", "1"},
	}
	for _, tc := range cases {
		for i := 0; i < 3; i++ {
			var got string
			if err := s.db.QueryRowContext(ctx, "PRAGMA "+tc.pragma).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("pragma %s on fresh connection %d: got %q want %q", tc.pragma, i, got, tc.want)
			}
		}
	}
}
