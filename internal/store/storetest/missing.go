package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunMissingRows(t *testing.T, newStore func(*testing.T) store.DataStore) {
	ds := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	missing := store.NewID("missing")
	cases := []struct {
		name string
		call func() error
	}{
		{"UpdateAgent", func() error {
			return ds.UpdateAgent(ctx, &store.Agent{ID: missing, Name: "n", Status: store.AgentActive})
		}},
		{"DeleteAgent", func() error { return ds.DeleteAgent(ctx, missing) }},
		{"TouchAgent", func() error { return ds.TouchAgent(ctx, missing, now) }},
		{"DeleteEndpoint", func() error { return ds.DeleteEndpoint(ctx, missing) }},
		{"UpdateUser", func() error { return ds.UpdateUser(ctx, &store.User{ID: missing, Email: missing + "@example.com"}) }},
		{"UpdateMembership", func() error {
			return ds.UpdateMembership(ctx, &store.Membership{OrgID: missing, UserID: missing, Role: "member"})
		}},
		{"DeleteMembership", func() error { return ds.DeleteMembership(ctx, missing, missing) }},
		{"DeleteDomain", func() error { return ds.DeleteDomain(ctx, missing, missing) }},
		{"SetDomainVerified", func() error { return ds.SetDomainVerified(ctx, missing, now) }},
		{"RenameGroup", func() error { return ds.RenameGroup(ctx, missing, missing, "n") }},
		{"DeleteGroup", func() error { return ds.DeleteGroup(ctx, missing, missing) }},
		{"RemoveGroupMember", func() error { return ds.RemoveGroupMember(ctx, missing, missing, store.GroupMemberUser, missing) }},
		{"DeleteServiceAccount", func() error { return ds.DeleteServiceAccount(ctx, missing, missing) }},
		{"DeleteWorkloadIdentity", func() error { return ds.DeleteWorkloadIdentity(ctx, missing, missing, missing) }},
		{"DeleteInvite", func() error { return ds.DeleteInvite(ctx, missing, missing) }},
		{"SetMembershipDisabled", func() error { return ds.SetMembershipDisabled(ctx, missing, missing, &now) }},
		{"DeleteOrgSecret", func() error { return ds.DeleteOrgSecret(ctx, missing, missing) }},
		{"DeleteEgressCredential", func() error { return ds.DeleteEgressCredential(ctx, missing, missing) }},
		{"DeleteService", func() error { return ds.DeleteService(ctx, missing) }},
		{"DeleteConnection", func() error { return ds.DeleteConnection(ctx, missing) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%s on a missing row: want store.ErrNotFound, got %v", tc.name, err)
			}
		})
	}
	t.Run("idempotent bulk and session deletes", func(t *testing.T) {
		if err := ds.RevokeTokensByAgent(ctx, missing); err != nil {
			t.Fatalf("RevokeTokensByAgent: %v", err)
		}
		if err := ds.RevokeToken(ctx, missing); err != nil {
			t.Fatalf("RevokeToken: %v", err)
		}
		if err := ds.DeleteSession(ctx, missing); err != nil {
			t.Fatalf("DeleteSession: %v", err)
		}
		if err := ds.DeleteUserSessions(ctx, missing, missing); err != nil {
			t.Fatalf("DeleteUserSessions: %v", err)
		}
		if err := ds.DeleteExpiredSessions(ctx, now); err != nil {
			t.Fatalf("DeleteExpiredSessions: %v", err)
		}
	})
}
