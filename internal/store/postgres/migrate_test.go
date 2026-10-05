package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func freshDatabaseDSN(t *testing.T) string {
	t.Helper()
	adminDSN := os.Getenv("MISHMESH_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		t.Skip("MISHMESH_TEST_POSTGRES_DSN not set")
	}
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("mig_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)
		_ = admin.Close()
	})
	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func TestConcurrentOpenMigratesOnce(t *testing.T) {
	dsn := freshDatabaseDSN(t)
	const nodes = 8
	errs := make(chan error, nodes)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < nodes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := Open(dsn, PoolConfig{MaxOpenConns: 4})
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent open: %v", err)
		}
	}
}

func TestMigrationLockReleasedAfterOpen(t *testing.T) {
	dsn := freshDatabaseDSN(t)
	s, err := Open(dsn, PoolConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var acquired bool
	if err := s.db.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock($1)`, migrationLockKey).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("migration advisory lock still held after Open")
	}
}
