package controlplane

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type meBody struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	ActiveOrgID string `json:"active_org_id"`
	Role        string `json:"role"`
	Memberships []struct {
		OrgID   string `json:"org_id"`
		OrgName string `json:"org_name"`
		Role    string `json:"role"`
	} `json:"memberships"`
}

func newModeAPI(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "mode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true, SignupMode: mode})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newJarClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func register(t *testing.T, srv *httptest.Server, email string, want int) (*http.Client, meBody) {
	t.Helper()
	return registerTok(t, srv, email, "", want)
}

func registerTok(t *testing.T, srv *httptest.Server, email, token string, want int) (*http.Client, meBody) {
	t.Helper()
	c := newJarClient()
	var me meBody
	doc(t, c, srv, http.MethodPost, "/api/v1/auth/register", `{"email":"`+email+`","password":"supersecret","name":"N","invite_token":"`+token+`"}`, want, &me)
	return c, me
}

func invite(t *testing.T, owner *http.Client, srv *httptest.Server, email, role string) inviteDTO {
	t.Helper()
	var inv inviteDTO
	doc(t, owner, srv, http.MethodPost, "/api/v1/members", `{"email":"`+email+`","role":"`+role+`"}`, http.StatusCreated, &inv)
	return inv
}

func TestSignupModeOrgCreatesOrgPerUser(t *testing.T) {
	srv := newModeAPI(t, "")
	_, a := register(t, srv, "alice@example.com", http.StatusCreated)
	_, b := register(t, srv, "bob@example.com", http.StatusCreated)
	if a.ActiveOrgID == "" || a.ActiveOrgID == b.ActiveOrgID {
		t.Fatalf("each user needs own org: %q vs %q", a.ActiveOrgID, b.ActiveOrgID)
	}
	if a.Role != "owner" || b.Role != "owner" {
		t.Fatalf("roles: %q %q", a.Role, b.Role)
	}
	if len(a.Memberships) != 1 || a.Memberships[0].OrgName == "" {
		t.Fatalf("memberships: %+v", a.Memberships)
	}
}

func TestSignupModeInvite(t *testing.T) {
	srv := newModeAPI(t, "invite")
	owner, first := register(t, srv, "owner@example.com", http.StatusCreated)
	if first.ActiveOrgID != defaultOrgID || first.Role != "owner" {
		t.Fatalf("first user: %+v", first)
	}
	register(t, srv, "stranger@example.com", http.StatusForbidden)

	inv := invite(t, owner, srv, "invitee@example.com", "member")
	invitee, me := registerTok(t, srv, "invitee@example.com", inv.InviteToken, http.StatusCreated)
	if me.ActiveOrgID != defaultOrgID || me.Role != "member" {
		t.Fatalf("invitee: %+v", me)
	}
	doc(t, invitee, srv, http.MethodGet, "/api/v1/agents", "", http.StatusOK, nil)
}

func TestInviteeCannotLoginBeforeRegistering(t *testing.T) {
	srv := newModeAPI(t, "invite")
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
	invite(t, owner, srv, "invitee@example.com", "member")
	doc(t, newJarClient(), srv, http.MethodPost, "/api/v1/auth/login", `{"email":"invitee@example.com","password":""}`, http.StatusUnauthorized, nil)
}

func TestRemovedMemberLosesAccess(t *testing.T) {
	srv := newModeAPI(t, "invite")
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
	inv := invite(t, owner, srv, "m@example.com", "member")
	member, me := registerTok(t, srv, "m@example.com", inv.InviteToken, http.StatusCreated)
	doc(t, member, srv, http.MethodGet, "/api/v1/agents", "", http.StatusOK, nil)

	doc(t, owner, srv, http.MethodDelete, "/api/v1/members/"+me.ID, "", http.StatusNoContent, nil)
	doc(t, member, srv, http.MethodGet, "/api/v1/agents", "", http.StatusUnauthorized, nil)
	doc(t, member, srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusUnauthorized, nil)
}

func TestSwitchOrg(t *testing.T) {
	srv := newModeAPI(t, "")
	alice, a := register(t, srv, "alice@example.com", http.StatusCreated)
	_, b := register(t, srv, "bob@example.com", http.StatusCreated)

	var created struct {
		ID string `json:"id"`
	}
	doc(t, alice, srv, http.MethodPost, "/api/v1/orgs", `{"name":"second"}`, http.StatusCreated, &created)

	doc(t, alice, srv, http.MethodPost, "/api/v1/auth/switch-org", `{"org_id":"`+b.ActiveOrgID+`"}`, http.StatusNotFound, nil)

	var switched meBody
	doc(t, alice, srv, http.MethodPost, "/api/v1/auth/switch-org", `{"org_id":"`+created.ID+`"}`, http.StatusOK, &switched)
	if switched.ActiveOrgID != created.ID || len(switched.Memberships) != 2 {
		t.Fatalf("switched: %+v", switched)
	}
	var me meBody
	doc(t, alice, srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusOK, &me)
	if me.ActiveOrgID != created.ID {
		t.Fatalf("session not re-scoped: %+v", me)
	}
	doc(t, alice, srv, http.MethodPost, "/api/v1/agents", `{"name":"x"}`, http.StatusCreated, nil)
	var agents []agentDTO
	doc(t, alice, srv, http.MethodGet, "/api/v1/agents", "", http.StatusOK, &agents)
	if len(agents) != 1 || agents[0].OrgID != created.ID {
		t.Fatalf("agents: %+v (first org %s)", agents, a.ActiveOrgID)
	}
}

func TestRegisterValidatesEmail(t *testing.T) {
	srv := newModeAPI(t, "")
	for _, email := range []string{"nope", "a@", "@b.com", "a b@c.com", "A <a@b.com>", "a@b"} {
		register(t, srv, email, http.StatusBadRequest)
	}
}

func TestAuthRateLimited(t *testing.T) {
	srv := newModeAPI(t, "")
	register(t, srv, "alice@example.com", http.StatusCreated)
	limited := false
	for i := 0; i < 60; i++ {
		resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"email":"alice@example.com","password":"wrongwrong"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("expected 429 after repeated failed logins")
	}
}

func TestAuthConfigAdvertisesSignup(t *testing.T) {
	srv := newModeAPI(t, "invite")
	var cfg map[string]any
	doc(t, &http.Client{}, srv, http.MethodGet, "/api/v1/auth/config", "", http.StatusOK, &cfg)
	if cfg["signup_mode"] != "invite" || cfg["password_signup"] != true {
		t.Fatalf("config: %+v", cfg)
	}
}
