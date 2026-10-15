package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/migrationtest"
)

func legacyMigrationFixture(t *testing.T) (string, *secrets.Box) {
	t.Helper()
	t.Setenv("MISHMESH_TEST_RETAIN_FIXTURES", "true")
	dsn := freshDatabaseDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationtest.Populate(context.Background(), db, true, box); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dsn, box
}

func TestShouldRejectUnsupportedSchemaAcrossConcurrentPostgresOpens(t *testing.T) {
	dsn, _ := legacyMigrationFixture(t)
	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			data, err := Open(dsn, PoolConfig{MaxOpenConns: 2})
			if data != nil {
				data.Close()
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, store.ErrUnsupportedSchema) {
			t.Fatalf("unsupported schema: %v", err)
		}
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	for query, expected := range map[string]int{
		`SELECT COUNT(*) FROM cloudflare_connections`:                                                                     1,
		`SELECT COUNT(*) FROM services WHERE driver='exec'`:                                                               3,
		`SELECT COUNT(*) FROM endpoints WHERE policy LIKE '%public_access%'`:                                              13,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='connections'`: 0,
	} {
		if err := db.QueryRow(query).Scan(&count); err != nil || count != expected {
			t.Fatalf("database changed: %s count=%d err=%v", query, count, err)
		}
	}
}
