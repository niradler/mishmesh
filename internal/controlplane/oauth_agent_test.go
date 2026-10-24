package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

const agentVerifier = "verifier-verifier-verifier-verifier-verifier-1234"

func agentChallenge() string {
	sum := sha256.Sum256([]byte(agentVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizePath(redirect string, extra url.Values) string {
	q := url.Values{
		"client_id":             {identity.AgentClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"state":                 {"st4te"},
		"code_challenge":        {agentChallenge()},
		"code_challenge_method": {"S256"},
	}
	for k, v := range extra {
		q[k] = v
	}
	return "/oauth2/authorize?" + q.Encode()
}

func postForm(t *testing.T, f *browserFixture, path string, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(f.srv.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestAgentAuthorizationCodeFlow(t *testing.T) {
	f := newBrowserFixture(t)
	owner, me := register(t, f.srv, "owner@example.com", http.StatusCreated)
	redirect := "http://127.0.0.1:43123/callback"

	bad := []struct {
		name string
		path string
		want int
	}{
		{"unknown client", authorizePath(redirect, url.Values{"client_id": {"evil"}}), http.StatusBadRequest},
		{"non loopback redirect", authorizePath("http://evil.example:80/cb", nil), http.StatusBadRequest},
		{"localhost name redirect", authorizePath("http://localhost:43123/cb", nil), http.StatusBadRequest},
	}
	for _, tc := range bad {
		if resp := f.get(t, owner, tc.path); resp.StatusCode != tc.want {
			t.Fatalf("%s = %d", tc.name, resp.StatusCode)
		}
	}
	resp := f.get(t, owner, authorizePath(redirect, url.Values{"code_challenge_method": {"plain"}}))
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.Contains(loc, "error=invalid_request") || !strings.Contains(loc, "state=st4te") {
		t.Fatalf("plain pkce = %d %q", resp.StatusCode, loc)
	}
	resp = f.get(t, owner, authorizePath(redirect, url.Values{"org_id": {"org_other"}}))
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=access_denied") {
		t.Fatalf("foreign org = %q", loc)
	}
	resp = f.get(t, &http.Client{}, authorizePath(redirect, nil))
	if !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("anonymous = %q", resp.Header.Get("Location"))
	}

	resp = f.get(t, owner, authorizePath(redirect, url.Values{"state": {strings.Repeat("s", 600)}}))
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=invalid_request") {
		t.Fatalf("oversized state = %q", loc)
	}

	resp = f.get(t, owner, authorizePath(redirect, nil))
	consent, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || consent.Path != consentPath || consent.Query().Get("code") != "" {
		t.Fatalf("authorize must stop at consent: %d %v", resp.StatusCode, consent)
	}
	consentURL := "/api/v1/auth/consent/" + consent.Query().Get("request")
	doc(t, &http.Client{}, f.srv, http.MethodGet, consentURL, "", http.StatusUnauthorized, nil)
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/auth/consent/unknown", "", http.StatusNotFound, nil)
	var view map[string]any
	doc(t, owner, f.srv, http.MethodGet, consentURL, "", http.StatusOK, &view)
	if view["org_id"] != defaultOrgID || view["redirect_uri"] != redirect || view["client_id"] != identity.AgentClientID {
		t.Fatalf("consent view = %v", view)
	}
	doc(t, owner, f.srv, http.MethodPost, consentURL, `{}`, http.StatusBadRequest, nil)
	var decided map[string]string
	doc(t, owner, f.srv, http.MethodPost, consentURL, `{"approve":true}`, http.StatusOK, &decided)
	doc(t, owner, f.srv, http.MethodPost, consentURL, `{"approve":true}`, http.StatusNotFound, nil)
	loc, err := url.Parse(decided["redirect_to"])
	if err != nil || loc.Host != "127.0.0.1:43123" || loc.Query().Get("state") != "st4te" {
		t.Fatalf("consent redirect = %v", loc)
	}
	code := loc.Query().Get("code")
	exchange := url.Values{"grant_type": {grantAuthorizationCode}, "client_id": {identity.AgentClientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {agentVerifier}}

	wrong := url.Values{}
	for k, v := range exchange {
		wrong[k] = v
	}
	wrong.Set("client_id", "other")
	if status, _ := postForm(t, f, "/oauth2/token", wrong); status != http.StatusUnauthorized {
		t.Fatalf("wrong client = %d", status)
	}
	status, body := postForm(t, f, "/oauth2/token", exchange)
	if status != http.StatusOK || body["refresh_token"] == nil || body["org_id"] != defaultOrgID {
		t.Fatalf("exchange = %d %v", status, body)
	}
	if status, body := postForm(t, f, "/oauth2/token", exchange); status != http.StatusBadRequest || body["error"] != oauthInvalidGrant {
		t.Fatalf("replay = %d %v", status, body)
	}
	access := body["access_token"].(string)
	p, err := f.id.Authenticate(context.Background(), access, token.TypeAgentUser)
	if err != nil || p.Ref != (authz.EntityRef{Type: authz.EntityUser, ID: me.ID}) || p.AuthMethod != authz.AuthMethodPassword {
		t.Fatalf("principal = %+v %v", p, err)
	}

	status, refreshed := postForm(t, f, "/oauth2/token", url.Values{"grant_type": {grantRefreshToken}, "client_id": {identity.AgentClientID}, "refresh_token": {body["refresh_token"].(string)}})
	if status != http.StatusOK || refreshed["access_token"] == access {
		t.Fatalf("refresh = %d %v", status, refreshed)
	}
	if _, err := f.id.Authenticate(context.Background(), access, token.TypeAgentUser); err == nil {
		t.Fatal("refresh must retire the previous access token")
	}

	if status, _ := postForm(t, f, "/oauth2/revoke", url.Values{"client_id": {identity.AgentClientID}, "token": {refreshed["refresh_token"].(string)}}); status != http.StatusOK {
		t.Fatalf("revoke = %d", status)
	}
	if _, err := f.id.Authenticate(context.Background(), refreshed["access_token"].(string), token.TypeAgentUser); err == nil {
		t.Fatal("revoked session still authenticates")
	}
	if status, _ := postForm(t, f, "/oauth2/revoke", url.Values{"client_id": {identity.AgentClientID}, "token": {"garbage"}}); status != http.StatusOK {
		t.Fatalf("revoke garbage = %d", status)
	}
}

func TestAgentDeviceFlow(t *testing.T) {
	f := newBrowserFixture(t)
	owner, me := register(t, f.srv, "owner@example.com", http.StatusCreated)

	if status, _ := postForm(t, f, "/oauth2/device_authorization", url.Values{"client_id": {"evil"}}); status != http.StatusUnauthorized {
		t.Fatalf("unknown client = %d", status)
	}
	status, start := postForm(t, f, "/oauth2/device_authorization", url.Values{"client_id": {identity.AgentClientID}})
	if status != http.StatusOK || start["user_code"] == nil || start["interval"] != float64(5) {
		t.Fatalf("device start = %d %v", status, start)
	}
	userCode := start["user_code"].(string)
	poll := url.Values{"grant_type": {grantDeviceCode}, "client_id": {identity.AgentClientID}, "device_code": {start["device_code"].(string)}}

	doc(t, &http.Client{}, f.srv, http.MethodPost, "/api/v1/auth/device/approve", `{"user_code":"`+userCode+`"}`, http.StatusUnauthorized, nil)
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/device/approve", `{"user_code":"`+userCode+`","org_id":"org_other"}`, http.StatusNotFound, nil)
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/device/approve", `{"user_code":"ZZZZ-ZZZZ"}`, http.StatusNotFound, nil)
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/device/approve", `{"user_code":"`+userCode+`"}`, http.StatusNoContent, nil)
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/device/approve", `{"user_code":"`+userCode+`"}`, http.StatusNotFound, nil)

	status, body := postForm(t, f, "/oauth2/token", poll)
	if status != http.StatusOK || body["access_token"] == nil {
		t.Fatalf("poll = %d %v", status, body)
	}
	p, err := f.id.Authenticate(context.Background(), body["access_token"].(string), token.TypeAgentUser)
	if err != nil || p.Ref.ID != me.ID || p.OrgID != defaultOrgID {
		t.Fatalf("device principal = %+v %v", p, err)
	}
	if status, body := postForm(t, f, "/oauth2/token", poll); status != http.StatusBadRequest || body["error"] != oauthSlowDown {
		t.Fatalf("fast poll = %d %v", status, body)
	}
}

func TestAgentDeviceLookupAndDeny(t *testing.T) {
	f := newBrowserFixture(t)
	owner, _ := register(t, f.srv, "owner@example.com", http.StatusCreated)
	_, start := postForm(t, f, "/oauth2/device_authorization", url.Values{"client_id": {identity.AgentClientID}})
	userCode := start["user_code"].(string)
	poll := url.Values{"grant_type": {grantDeviceCode}, "client_id": {identity.AgentClientID}, "device_code": {start["device_code"].(string)}}
	lookup := "/api/v1/auth/device?" + url.Values{"user_code": {userCode}}.Encode()

	doc(t, &http.Client{}, f.srv, http.MethodGet, lookup, "", http.StatusUnauthorized, nil)
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/auth/device?user_code=ZZZZ-ZZZZ", "", http.StatusNotFound, nil)
	var view map[string]any
	doc(t, owner, f.srv, http.MethodGet, lookup, "", http.StatusOK, &view)
	if view["user_code"] != userCode || view["client_id"] != identity.AgentClientID || view["expires_at"] == nil {
		t.Fatalf("device view = %v", view)
	}

	doc(t, &http.Client{}, f.srv, http.MethodPost, "/api/v1/auth/device/deny", `{"user_code":"`+userCode+`"}`, http.StatusUnauthorized, nil)
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/device/deny", `{"user_code":"`+userCode+`"}`, http.StatusNoContent, nil)
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/device/deny", `{"user_code":"`+userCode+`"}`, http.StatusNotFound, nil)
	doc(t, owner, f.srv, http.MethodPost, "/api/v1/auth/device/approve", `{"user_code":"`+userCode+`"}`, http.StatusNotFound, nil)

	if status, body := postForm(t, f, "/oauth2/token", poll); status != http.StatusBadRequest || body["error"] != oauthAccessDenied {
		t.Fatalf("denied poll = %d %v", status, body)
	}
}

func TestAgentRefreshFailsForDisabledMember(t *testing.T) {
	f := newBrowserFixture(t)
	_, _ = register(t, f.srv, "owner@example.com", http.StatusCreated)
	ctx := context.Background()
	if err := f.data.CreateUser(ctx, &store.User{ID: "usr_gone", Email: "gone@example.com", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	tokens, err := f.id.IssueAgentUser(ctx, identity.AgentLogin{OrgID: defaultOrgID, UserID: "usr_gone", AuthMethod: authz.AuthMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	status, body := postForm(t, f, "/oauth2/token", url.Values{"grant_type": {grantRefreshToken}, "client_id": {identity.AgentClientID}, "refresh_token": {tokens.RefreshToken}})
	if status != http.StatusBadRequest || body["error"] != oauthInvalidGrant {
		t.Fatalf("non-member refresh = %d %v", status, body)
	}
	if _, err := f.id.Authenticate(ctx, tokens.AccessToken, token.TypeAgentUser); err == nil {
		t.Fatal("non-member session must be revoked")
	}
}
