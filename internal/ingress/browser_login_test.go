package ingress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

const testAPIURL = "https://api.example.com"

func browserEndpoint() *store.Endpoint {
	return identityEndpoint(store.EndpointIdentity{Required: true, BrowserLogin: true})
}

func putLogin(t *testing.T, svc *identity.Service, l identity.EndpointLogin) string {
	t.Helper()
	code, err := svc.PutEndpointLogin(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func sessionCookieFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == endpointSessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie set: %d %s", endpointSessionCookie, w.Code, w.Body)
	return nil
}

func TestEndpointCallbackMintsSession(t *testing.T) {
	svc := newIdentityService(t)
	ing := &Ingress{log: discardLogger(), identity: svc, deciders: &fakeDeciders{decide: allowAll}, apiURL: testAPIURL}
	ep := browserEndpoint()
	login := identity.EndpointLogin{EndpointID: "ep_1", OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodOIDC, AuthTime: time.Now().UTC(), Return: "/tunnel/ep_1/app?x=1"}
	code := putLogin(t, svc, login)

	w := httptest.NewRecorder()
	ing.handleEndpointCallback(w, httptest.NewRequest(http.MethodGet, "http://example.com/tunnel/ep_1"+identity.EndpointCallbackPath+"?code="+code, nil), ep)
	if w.Code != http.StatusFound || w.Header().Get("Location") != login.Return {
		t.Fatalf("callback = %d %q", w.Code, w.Header().Get("Location"))
	}
	c := sessionCookieFrom(t, w)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/tunnel/ep_1" || c.Domain != "" {
		t.Fatalf("cookie attributes = %+v", c)
	}

	replay := httptest.NewRecorder()
	ing.handleEndpointCallback(replay, httptest.NewRequest(http.MethodGet, "http://example.com/tunnel/ep_1"+identity.EndpointCallbackPath+"?code="+code, nil), ep)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code = %d", replay.Code)
	}

	deciders := &fakeDeciders{decide: allowAll}
	ing.deciders = deciders
	r := httptest.NewRequest(http.MethodGet, "http://example.com/tunnel/ep_1/app", nil)
	r.AddCookie(c)
	out, ok := ing.applyIdentityGate(httptest.NewRecorder(), r, ep, "/app")
	if !ok {
		t.Fatal("session cookie rejected")
	}
	p := identityFrom(out).principal
	if p.Ref != (authz.EntityRef{Type: authz.EntityUser, ID: "usr_1"}) || p.AuthMethod != authz.AuthMethodOIDC {
		t.Fatalf("principal = %+v", p)
	}
	if got := deciders.reqs[0].Context.AuthMethod; got != authz.AuthMethodOIDC {
		t.Fatalf("decision auth method = %q", got)
	}
}

func TestEndpointCallbackRejectsOtherEndpoint(t *testing.T) {
	svc := newIdentityService(t)
	ing := &Ingress{log: discardLogger(), identity: svc, apiURL: testAPIURL}
	code := putLogin(t, svc, identity.EndpointLogin{EndpointID: "ep_2", OrgID: "org_1", UserID: "usr_1", Return: "/"})
	w := httptest.NewRecorder()
	ing.handleEndpointCallback(w, httptest.NewRequest(http.MethodGet, "http://app.example.com"+identity.EndpointCallbackPath+"?code="+code, nil), browserEndpoint())
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code for other endpoint = %d", w.Code)
	}
	plain := identityEndpoint(store.EndpointIdentity{Required: true})
	w = httptest.NewRecorder()
	ing.handleEndpointCallback(w, httptest.NewRequest(http.MethodGet, "http://app.example.com"+identity.EndpointCallbackPath+"?code=x", nil), plain)
	if w.Code != http.StatusNotFound {
		t.Fatalf("callback on endpoint without browser login = %d", w.Code)
	}
}

func TestEndpointSessionCookieScope(t *testing.T) {
	svc := newIdentityService(t)
	issue := func(endpointID, org string) string {
		issued, err := svc.Issue(context.Background(), identity.IssueRequest{OrgID: org, Subject: authz.EntityRef{Type: authz.EntityUser, ID: "usr_1"}, Type: token.TypeSession, AuthMethod: authz.AuthMethodPassword, TTL: time.Hour, EndpointID: endpointID})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Token
	}
	tests := []struct {
		name   string
		cookie string
	}{
		{"other endpoint session", issue("ep_2", "org_1")},
		{"other org session", issue("ep_1", "org_2")},
		{"access token in cookie", issueSAToken(t, svc, "org_1")},
		{"garbage", "nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ing := &Ingress{log: discardLogger(), identity: svc, deciders: &fakeDeciders{decide: allowAll}, apiURL: testAPIURL}
			r := httptest.NewRequest(http.MethodGet, "http://app.example.com/x", nil)
			r.AddCookie(&http.Cookie{Name: endpointSessionCookie, Value: tc.cookie})
			w := httptest.NewRecorder()
			if _, ok := ing.applyIdentityGate(w, r, browserEndpoint(), "/x"); ok || w.Code != http.StatusFound {
				t.Fatalf("ok=%v code=%d", ok, w.Code)
			}
		})
	}
}

func TestBrowserLoginRedirects(t *testing.T) {
	svc := newIdentityService(t)
	stale := func(authz.DecisionRequest) (authz.Decision, error) {
		return authz.Decision{Reason: authz.ReasonAuthStale}, nil
	}
	deny := func(authz.DecisionRequest) (authz.Decision, error) {
		return authz.Decision{Reason: authz.ReasonPolicyDeny}, nil
	}
	userCookie := func(authAge time.Duration) *http.Cookie {
		issued, err := svc.Issue(context.Background(), identity.IssueRequest{OrgID: "org_1", Subject: authz.EntityRef{Type: authz.EntityUser, ID: "usr_1"}, Type: token.TypeSession, AuthMethod: authz.AuthMethodPassword, AuthTime: time.Now().Add(-authAge), TTL: time.Hour, EndpointID: "ep_1"})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: endpointSessionCookie, Value: issued.Token}
	}
	tests := []struct {
		name      string
		method    string
		apiURL    string
		cookie    bool
		authAge   time.Duration
		decide    func(authz.DecisionRequest) (authz.Decision, error)
		want      int
		wantForce bool
	}{
		{"anonymous get redirects", http.MethodGet, testAPIURL, false, 0, allowAll, http.StatusFound, false},
		{"anonymous post is 401", http.MethodPost, testAPIURL, false, 0, allowAll, http.StatusUnauthorized, false},
		{"no api url is 401", http.MethodGet, "", false, 0, allowAll, http.StatusUnauthorized, false},
		{"stale session forces reauth", http.MethodGet, testAPIURL, true, time.Hour, stale, http.StatusFound, true},
		{"fresh login that still needs reauth is 403", http.MethodGet, testAPIURL, true, time.Second, stale, http.StatusForbidden, false},
		{"plain deny is 403", http.MethodGet, testAPIURL, true, time.Hour, deny, http.StatusForbidden, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ing := &Ingress{log: discardLogger(), identity: svc, deciders: &fakeDeciders{decide: tc.decide}, apiURL: tc.apiURL}
			r := httptest.NewRequest(tc.method, "http://app.example.com/x?y=1", nil)
			if tc.cookie {
				r.AddCookie(userCookie(tc.authAge))
			}
			w := httptest.NewRecorder()
			if _, ok := ing.applyIdentityGate(w, r, browserEndpoint(), "/x"); ok || w.Code != tc.want {
				t.Fatalf("ok=%v code=%d, want %d", ok, w.Code, tc.want)
			}
			if tc.want != http.StatusFound {
				return
			}
			loc, err := url.Parse(w.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			q := loc.Query()
			if loc.Scheme+"://"+loc.Host+loc.Path != testAPIURL+identity.EndpointLoginPath || q.Get("endpoint") != "ep_1" || q.Get("return") != "/x?y=1" {
				t.Fatalf("location = %s", loc)
			}
			if (q.Get("force") == "1") != tc.wantForce {
				t.Fatalf("force = %q, want %v", q.Get("force"), tc.wantForce)
			}
		})
	}
}

func TestBrowserLoginHeaderTokenNeverRedirects(t *testing.T) {
	svc := newIdentityService(t)
	deny := func(authz.DecisionRequest) (authz.Decision, error) {
		return authz.Decision{Reason: authz.ReasonAuthStale}, nil
	}
	ing := &Ingress{log: discardLogger(), identity: svc, deciders: &fakeDeciders{decide: deny}, apiURL: testAPIURL}
	r := httptest.NewRequest(http.MethodGet, "http://app.example.com/x", nil)
	r.Header.Set(identityAuthHeader, "Bearer "+issueSAToken(t, svc, "org_1"))
	w := httptest.NewRecorder()
	if _, ok := ing.applyIdentityGate(w, r, browserEndpoint(), "/x"); ok || w.Code != http.StatusForbidden {
		t.Fatalf("ok=%v code=%d", ok, w.Code)
	}
}
