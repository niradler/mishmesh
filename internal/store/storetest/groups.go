package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type groupFixture struct {
	ds    store.DataStore
	org   string
	other string
	user  string
	now   time.Time
}

func newGroupFixture(t *testing.T, ds store.DataStore) groupFixture {
	t.Helper()
	ctx := context.Background()
	f := groupFixture{ds: ds, org: store.NewID("org"), other: store.NewID("org"), user: store.NewID("usr"), now: time.Now().UTC().Truncate(time.Microsecond)}
	for _, id := range []string{f.org, f.other} {
		if err := ds.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: f.now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ds.CreateUser(ctx, &store.User{ID: f.user, Email: f.user + "@example.com", Name: "u", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{f.org, f.other} {
		if err := ds.CreateMembership(ctx, &store.Membership{OrgID: org, UserID: f.user, Role: "member", CreatedAt: f.now}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f groupFixture) group(t *testing.T, org, name string) *store.Group {
	t.Helper()
	g := &store.Group{ID: store.NewID("grp"), OrgID: org, Name: name, CreatedAt: f.now}
	if err := f.ds.CreateGroup(context.Background(), g); err != nil {
		t.Fatalf("create group %q: %v", name, err)
	}
	return g
}

func (f groupFixture) member(groupID, org string) *store.GroupMember {
	return &store.GroupMember{OrgID: org, GroupID: groupID, MemberType: store.GroupMemberUser, MemberID: f.user, CreatedAt: f.now}
}

func groupIDs(groups []*store.Group) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.ID
	}
	return out
}

func RunGroups(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()

	t.Run("crud round trip", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		g := f.group(t, f.org, "ops")
		got, err := f.ds.GetGroup(ctx, f.org, g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "ops" || got.OrgID != f.org || !got.CreatedAt.Equal(f.now) {
			t.Fatalf("group = %+v", got)
		}
		b := f.group(t, f.org, "admins")
		list, err := f.ds.ListGroups(ctx, f.org)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(groupIDs(list), []string{b.ID, g.ID}) {
			t.Fatalf("list not sorted by name: %v", groupIDs(list))
		}
		if err := f.ds.RenameGroup(ctx, f.org, g.ID, "sre"); err != nil {
			t.Fatal(err)
		}
		if got, _ := f.ds.GetGroup(ctx, f.org, g.ID); got.Name != "sre" {
			t.Fatalf("rename not applied: %+v", got)
		}
		if err := f.ds.DeleteGroup(ctx, f.org, g.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ds.GetGroup(ctx, f.org, g.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("get after delete = %v", err)
		}
	})

	t.Run("names unique per org", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		a := f.group(t, f.org, "ops")
		f.group(t, f.other, "ops")
		dup := &store.Group{ID: store.NewID("grp"), OrgID: f.org, Name: "ops", CreatedAt: f.now}
		if err := f.ds.CreateGroup(ctx, dup); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate create = %v, want ErrConflict", err)
		}
		f.group(t, f.org, "dev")
		if err := f.ds.RenameGroup(ctx, f.org, a.ID, "dev"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate rename = %v, want ErrConflict", err)
		}
	})

	t.Run("foreign org ids are not found", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		g := f.group(t, f.org, "ops")
		if err := f.ds.AddGroupMember(ctx, f.member(g.ID, f.org)); err != nil {
			t.Fatal(err)
		}
		tests := []struct {
			name string
			call func() error
		}{
			{"get", func() error { _, err := f.ds.GetGroup(ctx, f.other, g.ID); return err }},
			{"rename", func() error { return f.ds.RenameGroup(ctx, f.other, g.ID, "x") }},
			{"delete", func() error { return f.ds.DeleteGroup(ctx, f.other, g.ID) }},
			{"add member", func() error { return f.ds.AddGroupMember(ctx, f.member(g.ID, f.other)) }},
			{"remove member", func() error {
				return f.ds.RemoveGroupMember(ctx, f.other, g.ID, store.GroupMemberUser, f.user)
			}},
			{"unknown group", func() error { _, err := f.ds.GetGroup(ctx, f.org, "grp_missing"); return err }},
			{"rename unknown", func() error { return f.ds.RenameGroup(ctx, f.org, "grp_missing", "x") }},
			{"delete unknown", func() error { return f.ds.DeleteGroup(ctx, f.org, "grp_missing") }},
			{"add to unknown", func() error { return f.ds.AddGroupMember(ctx, f.member("grp_missing", f.org)) }},
			{"remove non member", func() error {
				return f.ds.RemoveGroupMember(ctx, f.org, g.ID, store.GroupMemberUser, "usr_missing")
			}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if err := tt.call(); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
			})
		}
		members, err := f.ds.ListGroupMembers(ctx, f.other, g.ID)
		if err != nil || len(members) != 0 {
			t.Fatalf("foreign member list = %v, %v", members, err)
		}
		groups, err := f.ds.ListMemberGroups(ctx, f.other, store.GroupMemberUser, f.user)
		if err != nil || len(groups) != 0 {
			t.Fatalf("foreign member groups = %v, %v", groups, err)
		}
		if _, err := f.ds.GetGroup(ctx, f.org, g.ID); err != nil {
			t.Fatalf("foreign calls changed the group: %v", err)
		}
	})

	t.Run("membership", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		ops := f.group(t, f.org, "ops")
		dev := f.group(t, f.org, "dev")
		for _, g := range []*store.Group{ops, dev} {
			if err := f.ds.AddGroupMember(ctx, f.member(g.ID, f.org)); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.ds.AddGroupMember(ctx, f.member(ops.ID, f.org)); err != nil {
			t.Fatalf("re-adding a member must be idempotent: %v", err)
		}
		sa := &store.GroupMember{OrgID: f.org, GroupID: ops.ID, MemberType: store.GroupMemberServiceAccount, MemberID: "sa_1", CreatedAt: f.now}
		if err := f.ds.AddGroupMember(ctx, sa); err != nil {
			t.Fatal(err)
		}
		members, err := f.ds.ListGroupMembers(ctx, f.org, ops.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(members) != 2 {
			t.Fatalf("members = %+v", members)
		}
		groups, err := f.ds.ListMemberGroups(ctx, f.org, store.GroupMemberUser, f.user)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(groupIDs(groups), []string{dev.ID, ops.ID}) {
			t.Fatalf("member groups = %v", groupIDs(groups))
		}
		if err := f.ds.RemoveGroupMember(ctx, f.org, ops.ID, store.GroupMemberUser, f.user); err != nil {
			t.Fatal(err)
		}
		groups, _ = f.ds.ListMemberGroups(ctx, f.org, store.GroupMemberUser, f.user)
		if !slices.Equal(groupIDs(groups), []string{dev.ID}) {
			t.Fatalf("after remove = %v", groupIDs(groups))
		}
	})

	t.Run("deleting a group removes its members", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		g := f.group(t, f.org, "ops")
		if err := f.ds.AddGroupMember(ctx, f.member(g.ID, f.org)); err != nil {
			t.Fatal(err)
		}
		if err := f.ds.DeleteGroup(ctx, f.org, g.ID); err != nil {
			t.Fatal(err)
		}
		groups, err := f.ds.ListMemberGroups(ctx, f.org, store.GroupMemberUser, f.user)
		if err != nil || len(groups) != 0 {
			t.Fatalf("member groups after delete = %v, %v", groupIDs(groups), err)
		}
		again := f.group(t, f.org, "ops")
		members, err := f.ds.ListGroupMembers(ctx, f.org, again.ID)
		if err != nil || len(members) != 0 {
			t.Fatalf("recreated group inherited members: %v, %v", members, err)
		}
	})

	t.Run("removing org membership removes group memberships in that org only", func(t *testing.T) {
		f := newGroupFixture(t, open(t))
		mine := f.group(t, f.org, "ops")
		theirs := f.group(t, f.other, "ops")
		for _, m := range []*store.GroupMember{f.member(mine.ID, f.org), f.member(theirs.ID, f.other)} {
			if err := f.ds.AddGroupMember(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.ds.DeleteMembership(ctx, f.org, f.user); err != nil {
			t.Fatal(err)
		}
		if groups, _ := f.ds.ListMemberGroups(ctx, f.org, store.GroupMemberUser, f.user); len(groups) != 0 {
			t.Fatalf("group memberships survived org removal: %v", groupIDs(groups))
		}
		if groups, _ := f.ds.ListMemberGroups(ctx, f.other, store.GroupMemberUser, f.user); len(groups) != 1 {
			t.Fatalf("other org memberships removed: %v", groupIDs(groups))
		}
	})
}
