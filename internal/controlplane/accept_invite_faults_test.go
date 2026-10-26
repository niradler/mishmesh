package controlplane

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func (f *faultStore) ClaimInviteCreatingMembership(ctx context.Context, inv *store.Invite, userID string, createdAt time.Time) error {
	if f.failCreateMember.Load() {
		return errInjected
	}
	return f.DataStore.ClaimInviteCreatingMembership(ctx, inv, userID, createdAt)
}

func (f *faultStore) CreateInvite(ctx context.Context, inv *store.Invite) error {
	if f.failCreateInvite.Load() {
		return errInjected
	}
	return f.DataStore.CreateInvite(ctx, inv)
}

func TestFailedAcceptInviteDoesNotBurnInvite(t *testing.T) {
	srv, api, data := newFaultAPI(t, "org")
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
	existing, _ := register(t, srv, "existing@example.com", http.StatusCreated)
	inv := invite(t, owner, srv, "existing@example.com", "member")
	body := `{"invite_token":"` + inv.InviteToken + `"}`

	data.failCreateMember.Store(true)
	data.failCreateInvite.Store(true)
	doc(t, existing, srv, http.MethodPost, "/api/v1/auth/accept-invite", body, http.StatusInternalServerError, nil)

	if got := liveInvites(t, api, "existing@example.com"); got != 1 {
		t.Fatalf("invite count after failed accept = %d, want 1", got)
	}
	data.failCreateMember.Store(false)
	doc(t, existing, srv, http.MethodPost, "/api/v1/auth/accept-invite", body, http.StatusOK, nil)
}

func TestFailedVerifiedEmailRedeemDoesNotBurnInvite(t *testing.T) {
	srv, api, data := newFaultAPI(t, "org")
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
	invite(t, owner, srv, "existing@example.com", "member")
	ctx := context.Background()
	user := &store.User{ID: store.NewID("usr"), Email: "existing@example.com", Name: "e"}
	if err := data.DataStore.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	data.failCreateMember.Store(true)
	data.failCreateInvite.Store(true)
	if err := api.redeemVerifiedEmailInvites(ctx, user); err == nil {
		t.Fatal("redeem swallowed the membership failure")
	}
	if got := liveInvites(t, api, "existing@example.com"); got != 1 {
		t.Fatalf("invite count after failed redeem = %d, want 1", got)
	}
}
