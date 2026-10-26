package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestOpenDoesNotExposeDatabasePassword(t *testing.T) {
	_, err := Open("postgres://user:private-database-password@localhost:invalid/database", PoolConfig{})
	if err == nil {
		t.Fatal("invalid database configuration accepted")
	}
	if strings.Contains(err.Error(), "private-database-password") {
		t.Fatal("database password exposed in startup error")
	}
}

func TestQueriesCancelWhenDatabaseIsStalled(t *testing.T) {
	s := newStoreWithPool(t, PoolConfig{QueryTimeout: 100 * time.Millisecond})
	transaction, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(context.Background(), "LOCK TABLE orgs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{"read", func(ctx context.Context) error { _, err := s.GetOrg(ctx, "missing"); return err }},
		{"list", func(ctx context.Context) error { _, err := s.ListOrgs(ctx); return err }},
		{"write", func(ctx context.Context) error {
			return s.CreateOrg(ctx, &store.Org{ID: "blocked-write", Name: "blocked", CreatedAt: time.Now()})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			err := test.run(context.Background())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want deadline exceeded, got %v", err)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("query cancellation took %s", elapsed)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := s.GetOrg(ctx, "missing"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("parent deadline not preserved: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 80*time.Millisecond {
		t.Fatalf("parent deadline extended: %s", elapsed)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListOrgs(context.Background()); err != nil {
		t.Fatalf("query did not recover: %v", err)
	}
}

func TestServiceSnapshotMutationCancelsWhenNetworkLockIsStalled(t *testing.T) {
	s := newStoreWithPool(t, PoolConfig{QueryTimeout: 100 * time.Millisecond})
	holder, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", networkWriteLockKey); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", networkWriteLockKey)

	guard, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	err = s.WithServiceSnapshots(guard, "org_stalled", nil, func(store.ServiceMutation) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("service snapshot mutation ignored the query timeout for %s", elapsed)
	}
}
