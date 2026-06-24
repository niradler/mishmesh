package controlplane

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newAuthAPI(t *testing.T) *httptest.Server {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "authz.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("localhost:8080", "http")
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func registerClient(t *testing.T, srv *httptest.Server, email, role string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	doc(t, client, srv, http.MethodPost, "/api/v1/auth/register",
		`{"email":"`+email+`","password":"supersecret","name":"x"}`, http.StatusCreated, nil)
	var me struct {
		Role string `json:"role"`
	}
	doc(t, client, srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusOK, &me)
	if me.Role != role {
		t.Fatalf("%s: want role %q got %q", email, role, me.Role)
	}
	return client
}

func TestMemberReadOnlyByDefault(t *testing.T) {
	srv := newAuthAPI(t)
	owner := registerClient(t, srv, "owner@example.com", "owner")
	member := registerClient(t, srv, "member@example.com", "member")

	doc(t, owner, srv, http.MethodPost, "/api/v1/agents", `{"name":"web"}`, http.StatusCreated, nil)

	doc(t, member, srv, http.MethodGet, "/api/v1/agents", "", http.StatusOK, nil)
	doc(t, member, srv, http.MethodPost, "/api/v1/agents", `{"name":"nope"}`, http.StatusForbidden, nil)
	doc(t, member, srv, http.MethodGet, "/api/v1/quota", "", http.StatusOK, nil)
	doc(t, member, srv, http.MethodPut, "/api/v1/quota", `{"max_agents":5}`, http.StatusForbidden, nil)
	doc(t, member, srv, http.MethodGet, "/api/v1/policy", "", http.StatusOK, nil)
	doc(t, member, srv, http.MethodPut, "/api/v1/policy", `{"matrix":{"member":["agent:write"]}}`, http.StatusForbidden, nil)
}

func TestPolicyDefaultMatrix(t *testing.T) {
	srv := newAuthAPI(t)
	owner := registerClient(t, srv, "owner@example.com", "owner")

	var pol policyDTO
	doc(t, owner, srv, http.MethodGet, "/api/v1/policy", "", http.StatusOK, &pol)
	if !pol.IsDefault {
		t.Error("fresh org should report default policy")
	}
	if !pol.Matrix["member"]["agent:read"] || pol.Matrix["member"]["agent:write"] {
		t.Errorf("member default should be read-only: %+v", pol.Matrix["member"])
	}
	if !pol.Matrix["owner"]["policy:write"] {
		t.Error("owner should have policy:write by default")
	}
}

func TestPolicyEditGrantsMemberWrite(t *testing.T) {
	srv := newAuthAPI(t)
	owner := registerClient(t, srv, "owner@example.com", "owner")
	member := registerClient(t, srv, "member@example.com", "member")

	doc(t, member, srv, http.MethodPost, "/api/v1/agents", `{"name":"nope"}`, http.StatusForbidden, nil)

	var updated policyDTO
	doc(t, owner, srv, http.MethodPut, "/api/v1/policy",
		`{"matrix":{"owner":["agent:read","agent:write","endpoint:read","endpoint:write","quota:read","quota:write","member:read","member:manage","audit:read","status:read","policy:read","policy:write"],"member":["agent:read","agent:write"]}}`,
		http.StatusOK, &updated)
	if updated.IsDefault {
		t.Error("edited policy should not be default")
	}
	if !updated.Matrix["member"]["agent:write"] {
		t.Error("member should now have agent:write")
	}

	doc(t, member, srv, http.MethodPost, "/api/v1/agents", `{"name":"yes"}`, http.StatusCreated, nil)
}

func TestPolicyRejectsUnknownAction(t *testing.T) {
	srv := newAuthAPI(t)
	owner := registerClient(t, srv, "owner@example.com", "owner")
	doc(t, owner, srv, http.MethodPut, "/api/v1/policy", `{"matrix":{"member":["bogus:action"]}}`, http.StatusBadRequest, nil)
}
