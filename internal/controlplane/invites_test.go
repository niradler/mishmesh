package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newInviteAPI(t *testing.T, mode string) (*httptest.Server, *API) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "invite.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "admin-secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true, SignupMode: mode})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, api
}

func isMember(t *testing.T, api *API, orgID, email string) (string, bool) {
	t.Helper()
	user, err := api.data.GetUserByEmail(context.Background(), email)
	if err != nil {
		return "", false
	}
	m, err := api.data.GetMembership(context.Background(), orgID, user.ID)
	if err != nil {
		return "", false
	}
	return m.Role, true
}

func TestInviteRegistrationRequiresValidToken(t *testing.T) {
	for _, mode := range []string{"org", "invite"} {
		t.Run(mode, func(t *testing.T) {
			srv, api := newInviteAPI(t, mode)
			owner, ownerMe := register(t, srv, "owner@example.com", http.StatusCreated)
			orgID := ownerMe.ActiveOrgID

			expired := &store.Invite{
				ID: store.NewID("inv"), TokenHash: store.HashToken("expired-token"), OrgID: orgID,
				Email: "late@example.com", Role: store.RoleMember, InvitedBy: ownerMe.ID,
				CreatedAt: time.Now().Add(-10 * 24 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour),
			}
			if err := api.data.CreateInvite(context.Background(), expired); err != nil {
				t.Fatal(err)
			}

			good := invite(t, owner, srv, "good@example.com", "admin")
			other := invite(t, owner, srv, "other@example.com", "member")
			invite(t, owner, srv, "notoken@example.com", "member")

			noTokenStatus := http.StatusForbidden
			if mode == "org" {
				noTokenStatus = http.StatusCreated
			}
			rejects := []struct {
				name  string
				email string
				token string
				want  int
			}{
				{"no token", "notoken@example.com", "", noTokenStatus},
				{"wrong token", "other@example.com", "not-a-real-token", http.StatusForbidden},
				{"expired token", "late@example.com", "expired-token", http.StatusForbidden},
				{"token for another email", "attacker@example.com", good.InviteToken, http.StatusForbidden},
			}
			for _, tc := range rejects {
				t.Run(tc.name, func(t *testing.T) {
					_, me := registerTok(t, srv, tc.email, tc.token, tc.want)
					if tc.want == http.StatusCreated && me.ActiveOrgID == orgID {
						t.Fatalf("joined invited org without a valid token: %+v", me)
					}
					if _, ok := isMember(t, api, orgID, tc.email); ok {
						t.Fatalf("%s must not be a member of the invited org", tc.email)
					}
				})
			}

			if _, ok := isMember(t, api, orgID, "good@example.com"); ok {
				t.Fatal("no-token registration must not have claimed the invite membership")
			}

			_, me := registerTok(t, srv, "good@example.com", good.InviteToken, http.StatusCreated)
			if me.ActiveOrgID != orgID || me.Role != "admin" {
				t.Fatalf("valid token must join with invited role: %+v", me)
			}
			if role, ok := isMember(t, api, orgID, "good@example.com"); !ok || role != "admin" {
				t.Fatalf("membership: %q %v", role, ok)
			}

			registerTok(t, srv, "other@example.com", good.InviteToken, http.StatusForbidden)
			if _, ok := isMember(t, api, orgID, "other@example.com"); ok {
				t.Fatal("reused token must not grant membership")
			}

			_, me = registerTok(t, srv, "other@example.com", other.InviteToken, http.StatusCreated)
			if me.Role != "member" {
				t.Fatalf("other: %+v", me)
			}
		})
	}
}

func TestInviteTokenIsStoredHashedAndSingleUse(t *testing.T) {
	srv, api := newInviteAPI(t, "org")
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
	inv := invite(t, owner, srv, "new@example.com", "member")
	if inv.InviteToken == "" || inv.InviteURL == "" || !inv.ExpiresAt.After(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("invite response: %+v", inv)
	}
	stored, err := api.data.GetInviteByTokenHash(context.Background(), store.HashToken(inv.InviteToken))
	if err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == inv.InviteToken {
		t.Fatal("raw token must not be stored")
	}
	if _, err := api.data.GetInviteByTokenHash(context.Background(), inv.InviteToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("raw token must not be a lookup key: %v", err)
	}

	var listed []inviteDTO
	doc(t, owner, srv, http.MethodGet, "/api/v1/invites", "", http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].InviteToken != "" {
		t.Fatalf("listing must not expose tokens: %+v", listed)
	}

	registerTok(t, srv, "new@example.com", inv.InviteToken, http.StatusCreated)
	doc(t, owner, srv, http.MethodGet, "/api/v1/invites", "", http.StatusOK, &listed)
	if len(listed) != 0 {
		t.Fatalf("redeemed invite must be consumed: %+v", listed)
	}
}

func TestInviteRevoke(t *testing.T) {
	srv, api := newInviteAPI(t, "invite")
	owner, ownerMe := register(t, srv, "owner@example.com", http.StatusCreated)
	inv := invite(t, owner, srv, "new@example.com", "member")
	doc(t, owner, srv, http.MethodDelete, "/api/v1/invites/"+inv.ID, "", http.StatusNoContent, nil)
	registerTok(t, srv, "new@example.com", inv.InviteToken, http.StatusForbidden)
	if _, ok := isMember(t, api, ownerMe.ActiveOrgID, "new@example.com"); ok {
		t.Fatal("revoked invite must not grant membership")
	}
}

func TestInviteCannotEscalateRole(t *testing.T) {
	srv, _ := newInviteAPI(t, "org")
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
	adminInv := invite(t, owner, srv, "admin@example.com", "admin")
	admin, _ := registerTok(t, srv, "admin@example.com", adminInv.InviteToken, http.StatusCreated)

	doc(t, admin, srv, http.MethodPost, "/api/v1/members", `{"email":"boss@example.com","role":"owner"}`, http.StatusForbidden, nil)
	doc(t, admin, srv, http.MethodPost, "/api/v1/members", `{"email":"peer@example.com","role":"admin"}`, http.StatusCreated, nil)

	var me meBody
	doc(t, admin, srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusOK, &me)
	doc(t, admin, srv, http.MethodPatch, "/api/v1/members/"+me.ID, `{"role":"owner"}`, http.StatusForbidden, nil)

	bearer := &http.Client{Transport: bearerTransport{token: "admin-secret"}}
	doc(t, bearer, srv, http.MethodPost, "/api/v1/members?org_id="+me.ActiveOrgID, `{"email":"boss@example.com","role":"owner"}`, http.StatusCreated, nil)
}

func TestAcceptInviteForExistingUser(t *testing.T) {
	srv, api := newInviteAPI(t, "org")
	owner, ownerMe := register(t, srv, "owner@example.com", http.StatusCreated)
	existing, _ := register(t, srv, "existing@example.com", http.StatusCreated)
	stranger, _ := register(t, srv, "stranger@example.com", http.StatusCreated)
	inv := invite(t, owner, srv, "existing@example.com", "member")

	if _, ok := isMember(t, api, ownerMe.ActiveOrgID, "existing@example.com"); ok {
		t.Fatal("an invite must not add an existing user before they accept")
	}
	body := `{"invite_token":"` + inv.InviteToken + `"}`
	doc(t, stranger, srv, http.MethodPost, "/api/v1/auth/accept-invite", body, http.StatusForbidden, nil)
	doc(t, newJarClient(), srv, http.MethodPost, "/api/v1/auth/accept-invite", body, http.StatusUnauthorized, nil)
	doc(t, existing, srv, http.MethodPost, "/api/v1/auth/accept-invite", `{"invite_token":"wrong"}`, http.StatusForbidden, nil)

	var me meBody
	doc(t, existing, srv, http.MethodPost, "/api/v1/auth/accept-invite", body, http.StatusOK, &me)
	if role, ok := isMember(t, api, ownerMe.ActiveOrgID, "existing@example.com"); !ok || role != "member" {
		t.Fatalf("membership: %q %v", role, ok)
	}
	if len(me.Memberships) != 2 {
		t.Fatalf("me: %+v", me)
	}
	doc(t, existing, srv, http.MethodPost, "/api/v1/auth/accept-invite", body, http.StatusForbidden, nil)
}

func TestOIDCInviteRedeemedOnlyWithVerifiedEmail(t *testing.T) {
	_, api := newInviteAPI(t, "invite")
	ctx := context.Background()
	owner := &store.User{ID: store.NewID("usr"), Email: "owner@example.com", PasswordHash: "x", CreatedAt: time.Now()}
	if err := api.data.CreateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := api.joinDefaultOrg(ctx, owner.ID, store.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := api.createInvite(ctx, defaultOrgID, "guest@example.com", store.RoleMember, owner.ID); err != nil {
		t.Fatal(err)
	}

	no, yes := false, true
	if _, err := api.upsertOIDCUser(ctx, &oidcProfile{Sub: "s1", Email: "guest@example.com", EmailVerified: &no}); !errors.Is(err, errEmailUnverified) {
		t.Fatalf("unverified email must not redeem: %v", err)
	}
	if _, ok := isMember(t, api, defaultOrgID, "guest@example.com"); ok {
		t.Fatal("unverified OIDC login must not join")
	}
	if _, err := api.upsertOIDCUser(ctx, &oidcProfile{Sub: "s2", Email: "stranger@example.com", EmailVerified: &yes}); !errors.Is(err, errSignupClosed) {
		t.Fatalf("verified but uninvited in invite mode: %v", err)
	}
	if _, err := api.upsertOIDCUser(ctx, &oidcProfile{Sub: "s1", Email: "guest@example.com", EmailVerified: &yes}); err != nil {
		t.Fatal(err)
	}
	if role, ok := isMember(t, api, defaultOrgID, "guest@example.com"); !ok || role != "member" {
		t.Fatalf("verified OIDC login must redeem: %q %v", role, ok)
	}
}
