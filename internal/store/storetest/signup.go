package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunSignupClaimingInvite(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	setup := func(t *testing.T, ds store.DataStore) *store.Invite {
		t.Helper()
		org := store.NewID("org")
		if err := ds.CreateOrg(ctx, &store.Org{ID: org, Name: org, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		inv := &store.Invite{ID: store.NewID("inv"), TokenHash: store.NewID("th"), OrgID: org, Email: store.NewID("e") + "@example.com", Role: store.RoleAdmin, InvitedBy: "x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := ds.CreateInvite(ctx, inv); err != nil {
			t.Fatal(err)
		}
		return inv
	}
	newUser := func(email string) *store.User {
		return &store.User{ID: store.NewID("usr"), Email: email, Name: "n", PasswordHash: "h", CreatedAt: now}
	}

	t.Run("creates user and membership and claims invite", func(t *testing.T) {
		ds := open(t)
		inv := setup(t, ds)
		u := newUser(inv.Email)
		if err := ds.CreateUserWithMembershipClaimingInvite(ctx, u, inv); err != nil {
			t.Fatal(err)
		}
		if got, err := ds.GetUserByEmail(ctx, inv.Email); err != nil || got.ID != u.ID {
			t.Fatalf("user = %+v %v", got, err)
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
		inv := setup(t, ds)
		if err := ds.DeleteInvite(ctx, inv.OrgID, inv.ID); err != nil {
			t.Fatal(err)
		}
		err := ds.CreateUserWithMembershipClaimingInvite(ctx, newUser(inv.Email), inv)
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("err = %v", err)
		}
		if _, err := ds.GetUserByEmail(ctx, inv.Email); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("user created despite conflict: %v", err)
		}
	})

	t.Run("failure rolls back the invite claim", func(t *testing.T) {
		ds := open(t)
		inv := setup(t, ds)
		taken := newUser(inv.Email)
		if err := ds.CreateUser(ctx, taken); err != nil {
			t.Fatal(err)
		}
		if err := ds.CreateUserWithMembershipClaimingInvite(ctx, newUser(inv.Email), inv); err == nil {
			t.Fatal("duplicate email signup succeeded")
		}
		if _, err := ds.GetInviteByTokenHash(ctx, inv.TokenHash); err != nil {
			t.Fatalf("invite burned by failed signup: %v", err)
		}
		if ms, _ := ds.ListMembershipsByOrg(ctx, inv.OrgID); len(ms) != 0 {
			t.Fatalf("memberships = %+v", ms)
		}
	})

	t.Run("concurrent redemption creates exactly one user", func(t *testing.T) {
		ds := open(t)
		for i := 0; i < 20; i++ {
			inv := setup(t, ds)
			const racers = 4
			errs := make([]error, racers)
			users := make([]*store.User, racers)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for j := 0; j < racers; j++ {
				users[j] = newUser(store.NewID("r") + "@example.com")
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[j] = ds.CreateUserWithMembershipClaimingInvite(ctx, users[j], inv)
				}()
			}
			close(start)
			wg.Wait()
			wins := 0
			for j, err := range errs {
				switch {
				case err == nil:
					wins++
				case errors.Is(err, store.ErrConflict):
					if _, gerr := ds.GetUserByID(ctx, users[j].ID); !errors.Is(gerr, store.ErrNotFound) {
						t.Fatalf("iteration %d: loser %d left an orphan user: %v", i, j, gerr)
					}
				default:
					t.Fatalf("iteration %d: unexpected err %v", i, err)
				}
			}
			ms, err := ds.ListMembershipsByOrg(ctx, inv.OrgID)
			if err != nil {
				t.Fatal(err)
			}
			if wins != 1 || len(ms) != 1 {
				t.Fatalf("iteration %d: wins=%d memberships=%d", i, wins, len(ms))
			}
		}
	})
}
