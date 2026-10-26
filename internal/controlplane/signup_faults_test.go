package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type faultStore struct {
	store.DataStore
	failCreateUser    atomic.Bool
	failUpdateUser    atomic.Bool
	failGetMembership atomic.Bool
	failCreateMember  atomic.Bool
}

func (f *faultStore) CreateUser(ctx context.Context, u *store.User) error {
	if f.failCreateUser.Load() {
		return errInjected
	}
	return f.DataStore.CreateUser(ctx, u)
}

func (f *faultStore) CreateUserWithMembershipClaimingInvite(ctx context.Context, u *store.User, inv *store.Invite) error {
	if f.failCreateUser.Load() || f.failCreateMember.Load() {
		return errInjected
	}
	return f.DataStore.CreateUserWithMembershipClaimingInvite(ctx, u, inv)
}

func (f *faultStore) UpdateUser(ctx context.Context, u *store.User) error {
	if f.failUpdateUser.Load() {
		return errInjected
	}
	return f.DataStore.UpdateUser(ctx, u)
}

func (f *faultStore) GetMembership(ctx context.Context, orgID, userID string) (*store.Membership, error) {
	if f.failGetMembership.Load() {
		return nil, errInjected
	}
	return f.DataStore.GetMembership(ctx, orgID, userID)
}

func (f *faultStore) CreateMembership(ctx context.Context, m *store.Membership) error {
	if f.failCreateMember.Load() {
		return errInjected
	}
	return f.DataStore.CreateMembership(ctx, m)
}

func newFaultAPI(t *testing.T, mode string) (*httptest.Server, *API, *faultStore) {
	t.Helper()
	sq, err := sqlite.Open(filepath.Join(t.TempDir(), "fault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sq.Close() })
	data := &faultStore{DataStore: sq}
	api := New(data, memory.NewConnStore(), "admin-secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true, SignupMode: mode})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, api, data
}

func liveInvites(t *testing.T, api *API, email string) int {
	t.Helper()
	invs, err := api.data.ListInvitesByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return len(invs)
}

func TestFailedSignupDoesNotBurnInvite(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*faultStore)
	}{
		{"user create fails", func(f *faultStore) { f.failCreateUser.Store(true) }},
		{"membership create fails", func(f *faultStore) { f.failCreateMember.Store(true) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, api, data := newFaultAPI(t, "invite")
			owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
			inv := invite(t, owner, srv, "new@example.com", "member")

			tc.inject(data)
			registerTok(t, srv, "new@example.com", inv.InviteToken, http.StatusInternalServerError)

			if got := liveInvites(t, api, "new@example.com"); got != 1 {
				t.Fatalf("invite count after failed signup = %d, want 1", got)
			}
			if u, err := api.data.GetUserByEmail(context.Background(), "new@example.com"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed signup left an orphan user %+v (err %v)", u, err)
			}
		})
	}
}

func TestGrantInviteMembershipSurfacesLookupError(t *testing.T) {
	_, api, data := newFaultAPI(t, "invite")
	ctx := context.Background()
	org, err := api.createOrg(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	user := &store.User{ID: store.NewID("usr"), Email: "m@example.com", Name: "m"}
	if err := data.DataStore.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	data.failGetMembership.Store(true)
	if err := api.grantInviteMembership(ctx, user.ID, &store.Invite{OrgID: org.ID, Role: store.RoleMember}); err == nil {
		t.Fatal("membership lookup failure was swallowed")
	}
}

func TestOIDCLinkSurfacesUpdateUserError(t *testing.T) {
	_, api, data := newFaultAPI(t, "org")
	user := &store.User{ID: store.NewID("usr"), Email: "g@example.com", Name: "g"}
	if err := data.DataStore.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	data.failUpdateUser.Store(true)
	verified := true
	_, err := api.upsertOIDCUser(context.Background(), &oidcProfile{Sub: "sub-1", Email: "g@example.com", EmailVerified: &verified})
	if err == nil {
		t.Fatal("UpdateUser failure was swallowed while linking the google account")
	}
}
