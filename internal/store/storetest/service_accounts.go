package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunServiceAccounts(t *testing.T, open func(t *testing.T) store.DataStore) {
	t.Run("crud", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		ctx := context.Background()
		sa := sampleServiceAccount(f.org, "ci", f.now)
		if err := f.ds.CreateServiceAccount(ctx, sa); err != nil {
			t.Fatal(err)
		}
		got, err := f.ds.GetServiceAccount(ctx, f.org, sa.ID)
		if err != nil {
			t.Fatal(err)
		}
		if *got != *sa {
			t.Fatalf("got %+v want %+v", got, sa)
		}
		byClient, err := f.ds.GetServiceAccountByClientID(ctx, sa.ClientID)
		if err != nil || byClient.ID != sa.ID {
			t.Fatalf("by client id = %+v %v", byClient, err)
		}
		sa.Name, sa.Role, sa.Disabled, sa.SecretHash = "ci-2", store.RoleAdmin, true, "newhash"
		sa.UpdatedAt = f.now.Add(time.Minute)
		if err := f.ds.UpdateServiceAccount(ctx, sa); err != nil {
			t.Fatal(err)
		}
		got, _ = f.ds.GetServiceAccount(ctx, f.org, sa.ID)
		if *got != *sa {
			t.Fatalf("after update got %+v want %+v", got, sa)
		}
		list, err := f.ds.ListServiceAccounts(ctx, f.org)
		if err != nil || len(list) != 1 {
			t.Fatalf("list = %v %v", list, err)
		}
		if other, _ := f.ds.ListServiceAccounts(ctx, f.other); len(other) != 0 {
			t.Fatalf("other org sees %v", other)
		}
	})

	t.Run("org scoped get update delete", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		ctx := context.Background()
		sa := sampleServiceAccount(f.org, "ci", f.now)
		_ = f.ds.CreateServiceAccount(ctx, sa)
		if _, err := f.ds.GetServiceAccount(ctx, f.other, sa.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-org get err = %v", err)
		}
		foreign := *sa
		foreign.OrgID = f.other
		if err := f.ds.UpdateServiceAccount(ctx, &foreign); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-org update err = %v", err)
		}
		if err := f.ds.DeleteServiceAccount(ctx, f.other, sa.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-org delete err = %v", err)
		}
	})

	t.Run("unique name and client id", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		ctx := context.Background()
		sa := sampleServiceAccount(f.org, "ci", f.now)
		_ = f.ds.CreateServiceAccount(ctx, sa)
		dupName := sampleServiceAccount(f.org, "ci", f.now)
		if err := f.ds.CreateServiceAccount(ctx, dupName); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup name err = %v", err)
		}
		dupClient := sampleServiceAccount(f.other, "ci", f.now)
		dupClient.ClientID = sa.ClientID
		if err := f.ds.CreateServiceAccount(ctx, dupClient); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("dup client id err = %v", err)
		}
	})

	t.Run("delete removes group memberships", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		ctx := context.Background()
		sa := sampleServiceAccount(f.org, "ci", f.now)
		_ = f.ds.CreateServiceAccount(ctx, sa)
		g := &store.Group{ID: store.NewID("grp"), OrgID: f.org, Name: "bots", CreatedAt: f.now}
		if err := f.ds.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		if err := f.ds.AddGroupMember(ctx, &store.GroupMember{OrgID: f.org, GroupID: g.ID, MemberType: store.GroupMemberServiceAccount, MemberID: sa.ID, CreatedAt: f.now}); err != nil {
			t.Fatal(err)
		}
		if err := f.ds.DeleteServiceAccount(ctx, f.org, sa.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ds.GetServiceAccount(ctx, f.org, sa.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("get after delete err = %v", err)
		}
		groups, err := f.ds.ListMemberGroups(ctx, f.org, store.GroupMemberServiceAccount, sa.ID)
		if err != nil || len(groups) != 0 {
			t.Fatalf("groups after delete = %v %v", groups, err)
		}
	})
}

func sampleServiceAccount(org, name string, now time.Time) *store.ServiceAccount {
	return &store.ServiceAccount{
		ID:         store.NewID("sa"),
		OrgID:      org,
		Name:       name,
		Role:       store.RoleMember,
		ClientID:   store.NewID("mmc"),
		SecretHash: store.HashToken("secret"),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}
