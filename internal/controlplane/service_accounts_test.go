package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/token"
)

const saAdminToken = "s3cret"

type saFixture struct {
	srv   *httptest.Server
	api   *API
	data  store.DataStore
	agent *streamAgent
}

func newSAFixture(t *testing.T) *saFixture {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "sa.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	for _, id := range []string{"org_a", "org_b"} {
		if err := data.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.CreateAgent(ctx, &store.Agent{ID: "ag_a", OrgID: "org_a", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	conns := memory.NewConnStore()
	agent := &streamAgent{id: "ag_a", failWith: "connection refused"}
	conns.AddAgent(agent)
	keys, err := token.NewKeyring(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	api := New(data, conns, saAdminToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetReachInEnabled(true)
	api.SetIdentity(identity.New(keys, memory.NewSessionStore()))
	api.SetAPIPublicURL("https://api.example.test/")
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &saFixture{srv: srv, api: api, data: data, agent: agent}
}

func (f *saFixture) call(t *testing.T, method, path, bearer, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

type createdSA struct {
	ServiceAccount serviceAccountDTO `json:"service_account"`
	ClientSecret   string            `json:"client_secret"`
}

func (f *saFixture) createSA(t *testing.T, org, name, role string) createdSA {
	t.Helper()
	code, body := f.call(t, http.MethodPost, "/api/v1/service-accounts?org_id="+org, saAdminToken, `{"name":"`+name+`","role":"`+role+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("create sa: %d %s", code, body)
	}
	var out createdSA
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.ServiceAccount.ClientID, "mmc_") || !strings.HasPrefix(out.ClientSecret, "mms_") || !out.ServiceAccount.HasSecret {
		t.Fatalf("created = %+v", out)
	}
	return out
}

func (f *saFixture) tokenRequest(t *testing.T, form url.Values, basicUser, basicPass string) (int, map[string]any, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicUser != "" {
		req.SetBasicAuth(url.QueryEscape(basicUser), url.QueryEscape(basicPass))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (f *saFixture) clientToken(t *testing.T, sa createdSA) string {
	t.Helper()
	code, out, hdr := f.tokenRequest(t, url.Values{"grant_type": {"client_credentials"}, "client_id": {sa.ServiceAccount.ClientID}, "client_secret": {sa.ClientSecret}}, "", "")
	if code != http.StatusOK {
		t.Fatalf("token: %d %v", code, out)
	}
	if hdr.Get("Cache-Control") != "no-store" || out["token_type"] != "Bearer" || out["expires_in"].(float64) != 3600 {
		t.Fatalf("token response = %v %v", out, hdr)
	}
	return out["access_token"].(string)
}

const reachBody = `{"target":"x:1","path":"/"}`

func TestServiceAccountClientCredentialsReachIn(t *testing.T) {
	f := newSAFixture(t)
	admin := f.createSA(t, "org_a", "deployer", "admin")
	member := f.createSA(t, "org_a", "reader", "member")
	foreign := f.createSA(t, "org_b", "deployer", "admin")

	adminTok := f.clientToken(t, admin)
	if code, body := f.call(t, http.MethodPost, "/api/v1/reach/ag_a/http", adminTok, reachBody); code != http.StatusBadGateway {
		t.Fatalf("admin sa reach: %d %s", code, body)
	}
	if code, _ := f.call(t, http.MethodPost, "/api/v1/reach/ag_a/http", f.clientToken(t, member), reachBody); code != http.StatusForbidden {
		t.Fatalf("member sa reach = %d, want 403", code)
	}
	if code, _ := f.call(t, http.MethodPost, "/api/v1/reach/ag_a/http?org_id=org_a", f.clientToken(t, foreign), reachBody); code != http.StatusNotFound {
		t.Fatalf("foreign org sa reach = %d, want 404", code)
	}
	if code, _ := f.call(t, http.MethodGet, "/api/v1/agents", adminTok, ""); code != http.StatusUnauthorized {
		t.Fatalf("sa token on management api = %d, want 401", code)
	}
	if code, _ := f.call(t, http.MethodPost, "/api/v1/reach/ag_a/http", adminTok+"x", reachBody); code != http.StatusUnauthorized {
		t.Fatalf("tampered token = %d, want 401", code)
	}

	basicCode, basicOut, _ := f.tokenRequest(t, url.Values{"grant_type": {"client_credentials"}}, admin.ServiceAccount.ClientID, admin.ClientSecret)
	if basicCode != http.StatusOK || basicOut["access_token"] == "" {
		t.Fatalf("basic auth token: %d %v", basicCode, basicOut)
	}
}

func TestServiceAccountRevocation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		suffix string
		body   string
	}{
		{"rotate", http.MethodPost, "/rotate", ""},
		{"disable", http.MethodPatch, "", `{"disabled":true}`},
		{"delete", http.MethodDelete, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSAFixture(t)
			sa := f.createSA(t, "org_a", "deployer", "admin")
			tok := f.clientToken(t, sa)
			path := "/api/v1/service-accounts/" + sa.ServiceAccount.ID + tc.suffix + "?org_id=org_a"
			if code, body := f.call(t, tc.method, path, saAdminToken, tc.body); code >= 300 {
				t.Fatalf("%s: %d %s", tc.name, code, body)
			}
			if code, _ := f.call(t, http.MethodPost, "/api/v1/reach/ag_a/http", tok, reachBody); code != http.StatusUnauthorized {
				t.Fatalf("token after %s = %d, want 401", tc.name, code)
			}
			code, out, _ := f.tokenRequest(t, url.Values{"grant_type": {"client_credentials"}, "client_id": {sa.ServiceAccount.ClientID}, "client_secret": {sa.ClientSecret}}, "", "")
			if code != http.StatusUnauthorized || out["error"] != "invalid_client" {
				t.Fatalf("old secret after %s: %d %v", tc.name, code, out)
			}
		})
	}
}

func TestOAuthTokenRejects(t *testing.T) {
	f := newSAFixture(t)
	sa := f.createSA(t, "org_a", "deployer", "admin")
	id, secret := sa.ServiceAccount.ClientID, sa.ClientSecret
	tests := []struct {
		name      string
		form      url.Values
		basicUser string
		basicPass string
		want      int
		wantErr   string
	}{
		{"missing grant", url.Values{"client_id": {id}, "client_secret": {secret}}, "", "", http.StatusBadRequest, "invalid_request"},
		{"unknown grant", url.Values{"grant_type": {"password"}}, "", "", http.StatusBadRequest, "unsupported_grant_type"},
		{"wrong secret", url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {"mms_nope"}}, "", "", http.StatusUnauthorized, "invalid_client"},
		{"unknown client", url.Values{"grant_type": {"client_credentials"}, "client_id": {"mmc_nope"}, "client_secret": {secret}}, "", "", http.StatusUnauthorized, "invalid_client"},
		{"no client", url.Values{"grant_type": {"client_credentials"}}, "", "", http.StatusUnauthorized, "invalid_client"},
		{"two auth methods", url.Values{"grant_type": {"client_credentials"}, "client_secret": {secret}}, id, secret, http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, hdr := f.tokenRequest(t, tc.form, tc.basicUser, tc.basicPass)
			if code != tc.want || out["error"] != tc.wantErr || hdr.Get("Cache-Control") != "no-store" {
				t.Fatalf("got %d %v %v", code, out, hdr)
			}
		})
	}

	big := url.Values{"grant_type": {"client_credentials"}, "pad": {strings.Repeat("a", 17<<10)}}
	if code, out, _ := f.tokenRequest(t, big, "", ""); code != http.StatusBadRequest {
		t.Fatalf("oversized body = %d %v", code, out)
	}
}

func TestServiceAccountAPINeverReturnsSecret(t *testing.T) {
	f := newSAFixture(t)
	sa := f.createSA(t, "org_a", "deployer", "member")
	for _, path := range []string{"/api/v1/service-accounts?org_id=org_a", "/api/v1/service-accounts/" + sa.ServiceAccount.ID + "?org_id=org_a"} {
		code, body := f.call(t, http.MethodGet, path, saAdminToken, "")
		if code != http.StatusOK || strings.Contains(string(body), sa.ClientSecret) || strings.Contains(string(body), "secret_hash") || !strings.Contains(string(body), `"has_secret":true`) {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
	}
	if code, _ := f.call(t, http.MethodGet, "/api/v1/service-accounts/"+sa.ServiceAccount.ID+"?org_id=org_b", saAdminToken, ""); code != http.StatusNotFound {
		t.Fatalf("cross-org get = %d", code)
	}
	for _, body := range []string{`{"name":"","role":"member"}`, `{"name":"x","role":"owner"}`, `{"name":" x","role":"member"}`} {
		if code, _ := f.call(t, http.MethodPost, "/api/v1/service-accounts?org_id=org_a", saAdminToken, body); code != http.StatusBadRequest {
			t.Fatalf("create %s = %d", body, code)
		}
	}
	if code, _ := f.call(t, http.MethodPost, "/api/v1/service-accounts?org_id=org_a", saAdminToken, `{"name":"deployer"}`); code != http.StatusConflict {
		t.Fatalf("duplicate = %d", code)
	}
}

func TestServiceAccountGroupMembership(t *testing.T) {
	f := newSAFixture(t)
	sa := f.createSA(t, "org_a", "deployer", "member")
	other := f.createSA(t, "org_b", "deployer", "member")
	code, body := f.call(t, http.MethodPost, "/api/v1/groups?org_id=org_a", saAdminToken, `{"name":"bots"}`)
	if code != http.StatusCreated {
		t.Fatalf("group: %d %s", code, body)
	}
	var g groupDTO
	_ = json.Unmarshal(body, &g)
	members := "/api/v1/groups/" + g.ID + "/members?org_id=org_a"
	if code, body := f.call(t, http.MethodPost, members, saAdminToken, `{"type":"service_account","id":"`+sa.ServiceAccount.ID+`"}`); code != http.StatusNoContent {
		t.Fatalf("add sa member: %d %s", code, body)
	}
	if code, _ := f.call(t, http.MethodPost, members, saAdminToken, `{"type":"service_account","id":"`+other.ServiceAccount.ID+`"}`); code != http.StatusNotFound {
		t.Fatalf("foreign sa member = %d, want 404", code)
	}
	groups, err := f.data.ListMemberGroups(context.Background(), "org_a", store.GroupMemberServiceAccount, sa.ServiceAccount.ID)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %v %v", groups, err)
	}
}

func TestOAuthMetadata(t *testing.T) {
	f := newSAFixture(t)
	code, body := f.call(t, http.MethodGet, "/.well-known/oauth-authorization-server", "", "")
	if code != http.StatusOK {
		t.Fatalf("metadata: %d %s", code, body)
	}
	var meta map[string]any
	_ = json.Unmarshal(body, &meta)
	if meta["issuer"] != "https://api.example.test" || meta["token_endpoint"] != "https://api.example.test/oauth2/token" {
		t.Fatalf("metadata = %v", meta)
	}
}
