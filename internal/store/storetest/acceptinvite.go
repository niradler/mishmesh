package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunClaimInviteCreatingMembership(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	setup := func(t *testing.T, ds store.DataStore) (*store.Invite, *store.User) {
		t.Helper()
		org := store.NewID("org")
		if err := ds.CreateOrg(ctx, &store.Org{ID: org, Name: org, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		u := &store.User{ID: store.NewID("usr"), Email: store.NewID("e") + "@example.com", Name: "n", PasswordHash: "h", CreatedAt: now}
		if err := ds.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		inv := &store.Invite{ID: store.NewID("inv"), TokenHash: store.NewID("th"), OrgID: org, Email: u.Email, Role: store.RoleAdmin, InvitedBy: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := ds.CreateInvite(ctx, inv); err != nil {
			t.Fatal(err)
		}
		return inv, u
	}

	t.Run("creates membership and claims invite", func(t *testing.T) {
		ds := open(t)
		inv, u := setup(t, ds)
		if err := ds.ClaimInviteCreatingMembership(ctx, inv, u.ID, now); err != nil {
			t.Fatal(err)
		}
		if m, err := ds.GetMembership(ctx, inv.OrgID, u.ID); err != nil || m.Role != store.RoleAdmin {
			t.Fatalf("membership = %+v %v", m, err)
		}
		if _, err := ds.GetInviteByTokenHash(ctx, inv.TokenHash); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("invite still present: %v", err)
		}
	})

	t.Run("already claimed invite conflicts and creates nothing", func(t *testing.T) {
		ds := open(t)
		inv, u := setup(t, ds)
		if err := ds.DeleteInvite(ctx, inv.OrgID, inv.ID); err != nil {
			t.Fatal(err)
		}
		if err := ds.ClaimInviteCreatingMembership(ctx, inv, u.ID, now); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("err = %v", err)
		}
		if _, err := ds.GetMembership(ctx, inv.OrgID, u.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("membership created despite conflict: %v", err)
		}
	})

	t.Run("existing member keeps role and the invite is consumed", func(t *testing.T) {
		ds := open(t)
		inv, u := setup(t, ds)
		if err := ds.CreateMembership(ctx, &store.Membership{OrgID: inv.OrgID, UserID: u.ID, Role: store.RoleOwner, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := ds.ClaimInviteCreatingMembership(ctx, inv, u.ID, now); err != nil {
			t.Fatal(err)
		}
		if m, _ := ds.GetMembership(ctx, inv.OrgID, u.ID); m.Role != store.RoleOwner {
			t.Fatalf("role = %s", m.Role)
		}
		if _, err := ds.GetInviteByTokenHash(ctx, inv.TokenHash); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("invite still present: %v", err)
		}
	})

	t.Run("failure rolls back the invite claim", func(t *testing.T) {
		ds := open(t)
		inv, _ := setup(t, ds)
		if err := ds.ClaimInviteCreatingMembership(ctx, inv, store.NewID("usr_missing"), now); err == nil {
			t.Fatal("membership for a missing user succeeded")
		}
		if _, err := ds.GetInviteByTokenHash(ctx, inv.TokenHash); err != nil {
			t.Fatalf("invite burned by failed accept: %v", err)
		}
	})

	t.Run("concurrent redemption claims exactly once", func(t *testing.T) {
		ds := open(t)
		for i := 0; i < 20; i++ {
			inv, u := setup(t, ds)
			const racers = 4
			errs := make([]error, racers)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for j := 0; j < racers; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[j] = ds.ClaimInviteCreatingMembership(ctx, inv, u.ID, now)
				}()
			}
			close(start)
			wg.Wait()
			wins := 0
			for _, err := range errs {
				switch {
				case err == nil:
					wins++
				case errors.Is(err, store.ErrConflict):
				default:
					t.Fatalf("iteration %d: unexpected err %v", i, err)
				}
			}
			if wins != 1 {
				t.Fatalf("iteration %d: wins=%d", i, wins)
			}
		}
	})
}
