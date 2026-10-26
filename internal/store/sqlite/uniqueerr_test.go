package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestUniqueErrMapsConstraintViolationsToConflict(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE probe (id TEXT PRIMARY KEY, name TEXT, other TEXT, UNIQUE(name))`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO probe VALUES ('a', 'n', 'x')`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		query string
		want  error
	}{
		{"primary key", `INSERT INTO probe VALUES ('a', 'm', 'x')`, store.ErrConflict},
		{"unique column", `INSERT INTO probe VALUES ('b', 'n', 'x')`, store.ErrConflict},
		{"not null", `INSERT INTO probe VALUES (NULL, 'q', 'x')`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.db.ExecContext(ctx, tc.query)
			if err == nil {
				if tc.want != nil {
					t.Fatal("expected a constraint violation")
				}
				return
			}
			got := uniqueErr("probe", err)
			if tc.want != nil && !errors.Is(got, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			if tc.want == nil && errors.Is(got, store.ErrConflict) {
				t.Fatalf("non-unique failure mapped to conflict: %v", got)
			}
		})
	}
}

func TestUniqueErrIgnoresMessageText(t *testing.T) {
	if errors.Is(uniqueErr("probe", errors.New("UNIQUE constraint failed: forged")), store.ErrConflict) {
		t.Fatal("a plain error with matching text must not be treated as a constraint violation")
	}
}
