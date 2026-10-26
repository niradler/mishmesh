package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func shouldGuardAuthorizedServiceSnapshots(t *testing.T, newStore func(*testing.T) store.DataStore) {
	s := newStore(t).(snapshotStore)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	original := CreateTypedService(t, s, createSnapshotOrg(t, s, ctx))
	snapshot, err := s.GetService(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := *snapshot
	changed.Config = json.RawMessage(`{"start":["privileged"]}`)
	if err := s.UpdateService(ctx, &changed); err != nil {
		t.Fatal(err)
	}
	called := false
	err = s.WithServiceSnapshots(ctx, snapshot.OrgID, []*store.Service{snapshot}, func(store.ServiceMutation) error { called = true; return nil })
	if !errors.Is(err, store.ErrConflict) || called {
		t.Fatalf("stale lease err=%v called=%v", err, called)
	}
	if err := s.UpdateServiceIfCurrent(ctx, snapshot, []*store.Service{snapshot}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	current, err := s.GetService(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(current.Config) != string(changed.Config) {
		t.Fatal("stale operation overwrote current configuration")
	}
	err = s.WithServiceSnapshots(ctx, "foreign", []*store.Service{current}, func(store.ServiceMutation) error { called = true; return nil })
	if !errors.Is(err, store.ErrConflict) || called {
		t.Fatalf("foreign lease err=%v called=%v", err, called)
	}
}

func shouldRollbackServiceMutationAndGuardTransitiveSnapshot(t *testing.T, newStore func(*testing.T) store.DataStore) {
	s := newStore(t).(snapshotStore)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	orgID := createSnapshotOrg(t, s, ctx)
	original := CreateTypedService(t, s, orgID)
	requirement := CreateTypedService(t, s, orgID)
	original.Requires = []string{requirement.ID}
	if err := s.UpdateService(ctx, original); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetService(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	requirementSnapshot, err := s.GetService(ctx, requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("persistence failed")
	err = s.WithServiceSnapshots(ctx, orgID, []*store.Service{snapshot, requirementSnapshot}, func(mutation store.ServiceMutation) error {
		changed := *snapshot
		changed.Name = "new-name"
		if err := mutation.UpdateService(ctx, &changed); err != nil {
			return err
		}
		rows, err := mutation.ListAllServices(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, row := range rows {
			found = found || row.ID == changed.ID && row.Name == changed.Name
		}
		if !found {
			t.Fatal("transaction read did not see saved definition")
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	unchanged, err := s.GetService(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !store.SameServiceInput(*snapshot, *unchanged) {
		t.Fatal("failed mutation was not rolled back")
	}
	requirement.Config = json.RawMessage(`{"start":["privileged"]}`)
	if err := s.UpdateService(ctx, requirement); err != nil {
		t.Fatal(err)
	}
	called := false
	err = s.WithServiceSnapshots(ctx, orgID, []*store.Service{snapshot, requirementSnapshot}, func(store.ServiceMutation) error { called = true; return nil })
	if !errors.Is(err, store.ErrConflict) || called {
		t.Fatalf("stale transitive auth: %v called=%v", err, called)
	}
}

func shouldSerializeServiceLeaseAgainstConcurrentMutation(t *testing.T, newStore func(*testing.T) store.DataStore) {
	s := newStore(t).(snapshotStore)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	original := CreateTypedService(t, s, createSnapshotOrg(t, s, ctx))
	snapshot, err := s.GetService(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	leased := make(chan error, 1)
	go func() {
		leased <- s.WithServiceSnapshots(ctx, snapshot.OrgID, []*store.Service{snapshot}, func(store.ServiceMutation) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	writerContext, writerCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer writerCancel()
	changed := *snapshot
	changed.Config = json.RawMessage(`{"start":["privileged"]}`)
	wrote := make(chan error, 1)
	go func() { wrote <- s.UpdateService(writerContext, &changed) }()
	select {
	case err := <-wrote:
		if err == nil {
			close(release)
			t.Fatal("writer changed a leased authorized definition")
		}
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	if err := <-leased; err != nil {
		t.Fatal(err)
	}
	current, err := s.GetService(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !store.SameServiceInput(*snapshot, *current) {
		t.Fatal("canceled writer mutated authorized snapshot")
	}
}

func createSnapshotOrg(t *testing.T, s store.DataStore, ctx context.Context) string {
	t.Helper()
	orgID := store.NewID("org")
	if err := s.CreateOrg(ctx, &store.Org{ID: orgID, Name: orgID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return orgID
}

type snapshotStore interface {
	store.DataStore
	store.ServiceSnapshotStore
}
