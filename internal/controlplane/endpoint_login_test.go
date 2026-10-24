package controlplane

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/token"
)

type browserFixture struct {
	srv  *httptest.Server
	api  *API
	data store.DataStore
	id   *identity.Service
}

func newBrowserFixture(t *testing.T) *browserFixture {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	keys, err := token.NewKeyring(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := identity.New(keys, memory.NewSessionStore())
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("example.com", "https")
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true, SignupMode: SignupModeInvite})
	api.SetIdentity(svc)
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	if err := data.CreateOrg(ctx, &store.Org{ID: "org_other", Name: "other", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return &browserFixture{srv: srv, api: api, data: data, id: svc}
}

func (f *browserFixture) endpoint(t *testing.T, id, org, sub string, idp *store.EndpointIdentity) {
	t.Helper()
	ctx := context.Background()
	agentID := "ag_" + id
	if err := f.data.CreateAgent(ctx, &store.Agent{ID: agentID, OrgID: org, Name: agentID, Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: id, AgentID: agentID, OrgID: org, Kind: store.KindHTTP, Subdomain: sub, CreatedAt: time.Now(), Policy: &store.EndpointPolicy{Identity: idp}}
	if err := f.data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
}

func noRedirect(c *http.Client) *http.Client {
	return &http.Client{Jar: c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (f *browserFixture) get(t *testing.T, c *http.Client, path string) *http.Response {
	t.Helper()
	resp, err := noRedirect(c).Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func loginPath(ep, ret string, force bool) string {
	q := url.Values{"endpoint": {ep}, "return": {ret}}
	if force {
		q.Set("force", "1")
	}
	return identity.EndpointLoginPath + "?" + q.Encode()
}

func TestEndpointLoginIssuesCode(t *testing.T) {
	f := newBrowserFixture(t)
	browser := &store.EndpointIdentity{Required: true, BrowserLogin: true}
	owner, me := register(t, f.srv, "owner@example.com", http.StatusCreated)
	f.endpoint(t, "ep_app", defaultOrgID, "app", browser)
	f.endpoint(t, "ep_plain", defaultOrgID, "plain", &store.EndpointIdentity{Required: true})
	f.endpoint(t, "ep_foreign", "org_other", "foreign", browser)

	jar, _ := cookiejar.New(nil)
	anon := &http.Client{Jar: jar}
	resp := f.get(t, anon, loginPath("ep_app", "/x", false))
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("anonymous = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp = f.get(t, owner, loginPath("ep_app", "/x?y=1", false))
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("owner = %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme != "https" || loc.Host != "app.example.com" || loc.Path != identity.EndpointCallbackPath {
		t.Fatalf("callback location = %s", loc)
	}
	login, err := f.id.TakeEndpointLogin(context.Background(), loc.Query().Get("code"))
	if err != nil {
		t.Fatal(err)
	}
	if login.EndpointID != "ep_app" || login.OrgID != defaultOrgID || login.UserID != me.ID || login.AuthMethod != authz.AuthMethodPassword || login.Return != "/x?y=1" {
		t.Fatalf("login = %+v", login)
	}

	tests := []struct {
		name string
		path string
		want int
	}{
		{"unknown endpoint", loginPath("ep_missing", "/", false), http.StatusNotFound},
		{"endpoint without browser login", loginPath("ep_plain", "/", false), http.StatusNotFound},
		{"other org endpoint", loginPath("ep_foreign", "/", false), http.StatusForbidden},
		{"absolute return", loginPath("ep_app", "https://evil.example/", false), http.StatusBadRequest},
		{"protocol relative return", loginPath("ep_app", "//evil.example/", false), http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if resp := f.get(t, owner, tc.path); resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestEndpointLoginForceRequiresFreshSession(t *testing.T) {
	f := newBrowserFixture(t)
	owner, _ := register(t, f.srv, "owner@example.com", http.StatusCreated)
	f.endpoint(t, "ep_app", defaultOrgID, "app", &store.EndpointIdentity{Required: true, BrowserLogin: true})

	if resp := f.get(t, owner, loginPath("ep_app", "/", true)); resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "https://app.example.com") {
		t.Fatalf("fresh session force = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	f.api.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	resp := f.get(t, owner, loginPath("ep_app", "/", true))
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("stale session force = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusUnauthorized, nil)
}

func TestSwitchOrgKeepsAuthTime(t *testing.T) {
	f := newBrowserFixture(t)
	owner, me := register(t, f.srv, "owner@example.com", http.StatusCreated)
	if err := f.data.CreateMembership(context.Background(), &store.Membership{OrgID: "org_other", UserID: me.ID, Role: store.RoleMember, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.api.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/switch-org", `{"org_id":"org_other"}`, http.StatusOK, nil)
	f.endpoint(t, "ep_other", "org_other", "other", &store.EndpointIdentity{Required: true, BrowserLogin: true})
	resp := f.get(t, owner, loginPath("ep_other", "/", true))
	if !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("switch-org refreshed auth time: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestDisableMemberRevokesSessions(t *testing.T) {
	f := newBrowserFixture(t)
	owner, ownerMe := register(t, f.srv, "owner@example.com", http.StatusCreated)
	inv := invite(t, owner, f.srv, "member@example.com", "member")
	member, me := registerTok(t, f.srv, "member@example.com", inv.InviteToken, http.StatusCreated)
	ctx := context.Background()
	issued, err := f.id.Issue(ctx, identity.IssueRequest{OrgID: defaultOrgID, Subject: authz.EntityRef{Type: authz.EntityUser, ID: me.ID}, Type: token.TypeSession, AuthMethod: authz.AuthMethodPassword, TTL: time.Hour, EndpointID: "ep_app"})
	if err != nil {
		t.Fatal(err)
	}

	doc(t, owner, f.srv, http.MethodPatch, "/api/v1/members/"+me.ID, `{}`, http.StatusBadRequest, nil)
	doc(t, owner, f.srv, http.MethodPatch, "/api/v1/members/"+ownerMe.ID, `{"disabled":true}`, http.StatusConflict, nil)
	doc(t, member, f.srv, http.MethodPatch, "/api/v1/members/"+ownerMe.ID, `{"disabled":true}`, http.StatusForbidden, nil)

	var out struct {
		Disabled bool   `json:"disabled"`
		Role     string `json:"role"`
	}
	doc(t, owner, f.srv, http.MethodPatch, "/api/v1/members/"+me.ID, `{"disabled":true}`, http.StatusOK, &out)
	if !out.Disabled || out.Role != store.RoleMember {
		t.Fatalf("patch = %+v", out)
	}
	doc(t, member, f.srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusUnauthorized, nil)
	if _, err := f.id.Authenticate(ctx, issued.Token, token.TypeSession); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("token session survived disable: %v", err)
	}
	doc(t, newJarClient(), f.srv, http.MethodPost, "/api/v1/auth/login", `{"email":"member@example.com","password":"supersecret"}`, http.StatusForbidden, nil)

	var members []memberDTO
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/members", "", http.StatusOK, &members)
	disabled := false
	for _, m := range members {
		if m.User["id"] == me.ID {
			disabled = m.Disabled
		}
	}
	if !disabled {
		t.Fatalf("members list does not show disabled: %+v", members)
	}

	subject, err := f.api.Decider(identity.Scope{OrgID: defaultOrgID}).(serverDecider).subject(ctx, authz.EntityRef{Type: authz.EntityUser, ID: me.ID})
	if err != nil || subject == nil || !subject.Disabled {
		t.Fatalf("decider subject = %+v, %v", subject, err)
	}

	doc(t, owner, f.srv, http.MethodPatch, "/api/v1/members/"+me.ID, `{"disabled":false}`, http.StatusOK, nil)
	doc(t, newJarClient(), f.srv, http.MethodPost, "/api/v1/auth/login", `{"email":"member@example.com","password":"supersecret"}`, http.StatusOK, nil)
}

func TestRemoveMemberRevokesTokenSessions(t *testing.T) {
	f := newBrowserFixture(t)
	owner, _ := register(t, f.srv, "owner@example.com", http.StatusCreated)
	inv := invite(t, owner, f.srv, "member@example.com", "member")
	_, me := registerTok(t, f.srv, "member@example.com", inv.InviteToken, http.StatusCreated)
	ctx := context.Background()
	issued, err := f.id.Issue(ctx, identity.IssueRequest{OrgID: defaultOrgID, Subject: authz.EntityRef{Type: authz.EntityUser, ID: me.ID}, Type: token.TypeSession, AuthMethod: authz.AuthMethodPassword, TTL: time.Hour, EndpointID: "ep_app"})
	if err != nil {
		t.Fatal(err)
	}
	doc(t, owner, f.srv, http.MethodDelete, "/api/v1/members/"+me.ID, "", http.StatusNoContent, nil)
	if _, err := f.id.Authenticate(ctx, issued.Token, token.TypeSession); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("token session survived removal: %v", err)
	}
}
