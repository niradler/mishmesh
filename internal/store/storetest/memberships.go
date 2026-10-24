package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunMembershipDisable(t *testing.T, open func(t *testing.T) store.DataStore) {
	t.Run("disable and enable membership", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		ctx := context.Background()
		at := f.now.Add(time.Minute)
		if err := f.ds.SetMembershipDisabled(ctx, f.org, f.user, &at); err != nil {
			t.Fatal(err)
		}
		m, err := f.ds.GetMembership(ctx, f.org, f.user)
		if err != nil || m.DisabledAt == nil || !m.DisabledAt.Equal(at) {
			t.Fatalf("membership = %+v %v", m, err)
		}
		other, _ := f.ds.GetMembership(ctx, f.other, f.user)
		if other.DisabledAt != nil {
			t.Fatalf("other org membership disabled: %+v", other)
		}
		list, _ := f.ds.ListMembershipsByOrg(ctx, f.org)
		if len(list) != 1 || list[0].DisabledAt == nil {
			t.Fatalf("list = %+v", list)
		}
		if err := f.ds.SetMembershipDisabled(ctx, f.org, f.user, nil); err != nil {
			t.Fatal(err)
		}
		if m, _ := f.ds.GetMembership(ctx, f.org, f.user); m.DisabledAt != nil {
			t.Fatalf("still disabled: %+v", m)
		}
		if err := f.ds.SetMembershipDisabled(ctx, f.org, "usr_missing", &at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing membership err = %v", err)
		}
	})

	t.Run("delete user sessions is org scoped", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		ctx := context.Background()
		mine := &store.Session{IDHash: store.NewID("sh"), UserID: f.user, OrgID: f.org, AuthMethod: "password", CreatedAt: f.now, ExpiresAt: f.now.Add(time.Hour)}
		kept := &store.Session{IDHash: store.NewID("sh"), UserID: f.user, OrgID: f.other, AuthMethod: "oidc", CreatedAt: f.now, ExpiresAt: f.now.Add(time.Hour)}
		for _, s := range []*store.Session{mine, kept} {
			if err := f.ds.CreateSession(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		got, err := f.ds.GetSession(ctx, kept.IDHash)
		if err != nil || got.AuthMethod != "oidc" {
			t.Fatalf("session = %+v %v", got, err)
		}
		if err := f.ds.DeleteUserSessions(ctx, f.org, f.user); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ds.GetSession(ctx, mine.IDHash); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("org session kept: %v", err)
		}
		if _, err := f.ds.GetSession(ctx, kept.IDHash); err != nil {
			t.Fatalf("other org session deleted: %v", err)
		}
	})
}
