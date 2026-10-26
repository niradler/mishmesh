package sqlite

import (
	"context"
	"testing"
)

func unindexedForeignKeys(t *testing.T, s *Store) []string {
	t.Helper()
	ctx := context.Background()
	tables, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	tables.Close()
	var missing []string
	for _, table := range names {
		leading := map[string]bool{}
		idx, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_index_list('`+table+`')`)
		if err != nil {
			t.Fatal(err)
		}
		var idxNames []string
		for idx.Next() {
			var n string
			if err := idx.Scan(&n); err != nil {
				t.Fatal(err)
			}
			idxNames = append(idxNames, n)
		}
		idx.Close()
		for _, n := range idxNames {
			var col string
			if err := s.db.QueryRowContext(ctx, `SELECT name FROM pragma_index_info('`+n+`') WHERE seqno=0`).Scan(&col); err == nil {
				leading[col] = true
			}
		}
		var pk string
		if err := s.db.QueryRowContext(ctx, `SELECT name FROM pragma_table_info('`+table+`') WHERE pk=1`).Scan(&pk); err == nil {
			leading[pk] = true
		}
		fks, err := s.db.QueryContext(ctx, `SELECT "from" FROM pragma_foreign_key_list('`+table+`') f WHERE seq = (SELECT MAX(seq) FROM pragma_foreign_key_list('`+table+`') g WHERE g.id = f.id)`)
		if err != nil {
			t.Fatal(err)
		}
		for fks.Next() {
			var col string
			if err := fks.Scan(&col); err != nil {
				t.Fatal(err)
			}
			if !leading[col] {
				missing = append(missing, table+"."+col)
			}
		}
		fks.Close()
	}
	return missing
}

func TestForeignKeyColumnsAreIndexed(t *testing.T) {
	s := newStore(t)
	if missing := unindexedForeignKeys(t, s); len(missing) > 0 {
		t.Fatalf("foreign key columns without a leading index: %v", missing)
	}
}
