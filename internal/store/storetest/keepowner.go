package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunKeepOwner(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	setup := func(t *testing.T, ds store.DataStore, roles ...string) (string, []string) {
		t.Helper()
		org := store.NewID("org")
		if err := ds.CreateOrg(ctx, &store.Org{ID: org, Name: org, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		var users []string
		for _, role := range roles {
			id := store.NewID("usr")
			if err := ds.CreateUser(ctx, &store.User{ID: id, Email: id + "@example.com", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := ds.CreateMembership(ctx, &store.Membership{OrgID: org, UserID: id, Role: role, CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			users = append(users, id)
		}
		return org, users
	}

	t.Run("last owner cannot be demoted or removed", func(t *testing.T) {
		ds := open(t)
		org, u := setup(t, ds, store.RoleOwner, store.RoleMember)
		if err := ds.DemoteOrRemoveMembershipKeepingOwner(ctx, org, u[0], store.RoleAdmin, false); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("demote last owner err = %v", err)
		}
		if err := ds.DemoteOrRemoveMembershipKeepingOwner(ctx, org, u[0], "", true); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("remove last owner err = %v", err)
		}
		if m, err := ds.GetMembership(ctx, org, u[0]); err != nil || m.Role != store.RoleOwner {
			t.Fatalf("membership = %+v %v", m, err)
		}
	})

	t.Run("allowed while another active owner remains", func(t *testing.T) {
		ds := open(t)
		org, u := setup(t, ds, store.RoleOwner, store.RoleOwner, store.RoleMember)
		if err := ds.DemoteOrRemoveMembershipKeepingOwner(ctx, org, u[0], store.RoleAdmin, false); err != nil {
			t.Fatal(err)
		}
		if m, _ := ds.GetMembership(ctx, org, u[0]); m.Role != store.RoleAdmin {
			t.Fatalf("role = %s", m.Role)
		}
		if err := ds.DemoteOrRemoveMembershipKeepingOwner(ctx, org, u[2], "", true); err != nil {
			t.Fatal(err)
		}
		if _, err := ds.GetMembership(ctx, org, u[2]); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("removed membership err = %v", err)
		}
		if err := ds.DemoteOrRemoveMembershipKeepingOwner(ctx, org, "usr_missing", "", true); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing err = %v", err)
		}
	})

	t.Run("disabled owner does not count as another owner", func(t *testing.T) {
		ds := open(t)
		org, u := setup(t, ds, store.RoleOwner, store.RoleOwner)
		at := now.Add(time.Minute)
		if err := ds.SetMembershipDisabled(ctx, org, u[1], &at); err != nil {
			t.Fatal(err)
		}
		if err := ds.DemoteOrRemoveMembershipKeepingOwner(ctx, org, u[0], store.RoleAdmin, false); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("concurrent mutual demotion leaves exactly one owner", func(t *testing.T) {
		ds := open(t)
		for i := 0; i < 40; i++ {
			org, u := setup(t, ds, store.RoleOwner, store.RoleOwner)
			var wg sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for j := 0; j < 2; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[j] = ds.DemoteOrRemoveMembershipKeepingOwner(ctx, org, u[j], store.RoleMember, false)
				}()
			}
			close(start)
			wg.Wait()
			succeeded, conflicted := 0, 0
			for _, err := range errs {
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, store.ErrConflict):
					conflicted++
				default:
					t.Fatalf("iteration %d: unexpected err %v", i, err)
				}
			}
			ms, err := ds.ListMembershipsByOrg(ctx, org)
			if err != nil {
				t.Fatal(err)
			}
			owners := 0
			for _, m := range ms {
				if m.Role == store.RoleOwner {
					owners++
				}
			}
			if succeeded != 1 || conflicted != 1 || owners != 1 {
				t.Fatalf("iteration %d: succeeded=%d conflicted=%d owners=%d", i, succeeded, conflicted, owners)
			}
		}
	})

	t.Run("disable keeps an active owner", func(t *testing.T) {
		ds := open(t)
		org, u := setup(t, ds, store.RoleOwner, store.RoleOwner, store.RoleMember)
		at := now.Add(time.Minute)
		if err := ds.SetMembershipDisabledKeepingOwner(ctx, org, u[2], &at); err != nil {
			t.Fatal(err)
		}
		if err := ds.SetMembershipDisabledKeepingOwner(ctx, org, u[0], &at); err != nil {
			t.Fatal(err)
		}
		if err := ds.SetMembershipDisabledKeepingOwner(ctx, org, u[1], &at); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("disable last active owner err = %v", err)
		}
		if m, _ := ds.GetMembership(ctx, org, u[1]); m.DisabledAt != nil {
			t.Fatalf("last owner disabled: %+v", m)
		}
		if err := ds.SetMembershipDisabledKeepingOwner(ctx, org, u[0], nil); err != nil {
			t.Fatal(err)
		}
		if err := ds.SetMembershipDisabledKeepingOwner(ctx, org, "usr_missing", &at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing err = %v", err)
		}
	})

	t.Run("concurrent mutual disable leaves exactly one active owner", func(t *testing.T) {
		ds := open(t)
		for i := 0; i < 40; i++ {
			org, u := setup(t, ds, store.RoleOwner, store.RoleOwner)
			at := now.Add(time.Minute)
			var wg sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for j := 0; j < 2; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[j] = ds.SetMembershipDisabledKeepingOwner(ctx, org, u[j], &at)
				}()
			}
			close(start)
			wg.Wait()
			succeeded, conflicted := 0, 0
			for _, err := range errs {
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, store.ErrConflict):
					conflicted++
				default:
					t.Fatalf("iteration %d: unexpected err %v", i, err)
				}
			}
			ms, err := ds.ListMembershipsByOrg(ctx, org)
			if err != nil {
				t.Fatal(err)
			}
			active := 0
			for _, m := range ms {
				if m.Role == store.RoleOwner && m.DisabledAt == nil {
					active++
				}
			}
			if succeeded != 1 || conflicted != 1 || active != 1 {
				t.Fatalf("iteration %d: succeeded=%d conflicted=%d active owners=%d", i, succeeded, conflicted, active)
			}
		}
	})
}
