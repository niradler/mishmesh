package ingress

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/token"
)

type fakeDeciders struct {
	decide func(authz.DecisionRequest) (authz.Decision, error)
	scopes []identity.Scope
	reqs   []authz.DecisionRequest
}

func (f *fakeDeciders) Decider(scope identity.Scope) authz.Decider {
	f.scopes = append(f.scopes, scope)
	return deciderFunc(func(_ context.Context, req authz.DecisionRequest) (authz.Decision, error) {
		f.reqs = append(f.reqs, req)
		return f.decide(req)
	})
}

type deciderFunc func(context.Context, authz.DecisionRequest) (authz.Decision, error)

func (d deciderFunc) Decide(ctx context.Context, req authz.DecisionRequest) (authz.Decision, error) {
	return d(ctx, req)
}

func allowAll(authz.DecisionRequest) (authz.Decision, error) {
	return authz.Decision{Allowed: true, Reason: authz.ReasonAllow}, nil
}

func newIdentityService(t *testing.T) *identity.Service {
	t.Helper()
	keys, err := token.NewKeyring(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return identity.New(keys, memory.NewSessionStore())
}

func issueSAToken(t *testing.T, svc *identity.Service, org string) string {
	t.Helper()
	issued, err := svc.Issue(context.Background(), identity.IssueRequest{
		OrgID:      org,
		Subject:    authz.EntityRef{Type: authz.EntityServiceAccount, ID: "sa_1"},
		Type:       token.TypeAccess,
		AuthMethod: authz.AuthMethodClientCredentials,
		TTL:        time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Token
}

func identityEndpoint(idp store.EndpointIdentity) *store.Endpoint {
	return &store.Endpoint{ID: "ep_1", OrgID: "org_1", Kind: store.KindHTTP, Policy: &store.EndpointPolicy{Identity: &idp}}
}

func TestIdentityGate(t *testing.T) {
	svc := newIdentityService(t)
	own := issueSAToken(t, svc, "org_1")
	foreign := issueSAToken(t, svc, "org_2")
	deny := func(authz.DecisionRequest) (authz.Decision, error) {
		return authz.Decision{Reason: authz.ReasonPolicyDeny}, nil
	}
	down := func(authz.DecisionRequest) (authz.Decision, error) {
		return authz.Decision{Reason: authz.ReasonUnavailable}, errors.New("down")
	}
	tests := []struct {
		name   string
		idp    store.EndpointIdentity
		header string
		value  string
		decide func(authz.DecisionRequest) (authz.Decision, error)
		want   int
	}{
		{"required without token", store.EndpointIdentity{Required: true}, "", "", allowAll, http.StatusUnauthorized},
		{"valid token allowed", store.EndpointIdentity{Required: true}, identityAuthHeader, "Bearer " + own, allowAll, 0},
		{"tampered token", store.EndpointIdentity{}, identityAuthHeader, "Bearer " + own + "x", allowAll, http.StatusUnauthorized},
		{"foreign org token", store.EndpointIdentity{Required: true}, identityAuthHeader, "Bearer " + foreign, allowAll, http.StatusNotFound},
		{"policy deny", store.EndpointIdentity{Required: true}, identityAuthHeader, "Bearer " + own, deny, http.StatusForbidden},
		{"anonymous deny asks for auth", store.EndpointIdentity{}, "", "", deny, http.StatusUnauthorized},
		{"anonymous allowed when optional", store.EndpointIdentity{}, "", "", allowAll, 0},
		{"decider unavailable", store.EndpointIdentity{Required: true}, identityAuthHeader, "Bearer " + own, down, http.StatusServiceUnavailable},
		{"authorization header ignored by default", store.EndpointIdentity{Required: true}, "Authorization", "Bearer " + own, allowAll, http.StatusUnauthorized},
		{"authorization header opt in", store.EndpointIdentity{Required: true, AuthorizationHeader: true}, "Authorization", "Bearer " + own, allowAll, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deciders := &fakeDeciders{decide: tc.decide}
			ing := &Ingress{log: discardLogger(), identity: svc, deciders: deciders}
			r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/x", nil)
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			out, ok := ing.applyIdentityGate(w, r, identityEndpoint(tc.idp), "/x")
			if tc.want == 0 {
				if !ok || identityFrom(out) == nil {
					t.Fatalf("rejected: %d %s", w.Code, w.Body)
				}
				return
			}
			if ok || w.Code != tc.want {
				t.Fatalf("ok=%v code=%d, want %d", ok, w.Code, tc.want)
			}
		})
	}
}

func TestIdentityGateDecisionInputs(t *testing.T) {
	svc := newIdentityService(t)
	deciders := &fakeDeciders{decide: allowAll}
	ing := &Ingress{log: discardLogger(), identity: svc, deciders: deciders}
	r := httptest.NewRequest(http.MethodPost, "http://demo.localhost/tunnel/ep_1/api", nil)
	r.Header.Set(identityAuthHeader, "Bearer "+issueSAToken(t, svc, "org_1"))
	if _, ok := ing.applyIdentityGate(httptest.NewRecorder(), r, identityEndpoint(store.EndpointIdentity{Required: true}), "/api"); !ok {
		t.Fatal("rejected")
	}
	if len(deciders.reqs) != 2 || deciders.reqs[0].Action != authz.ActionEndpointAccess || deciders.reqs[1].Action != authz.ActionHTTPRequest {
		t.Fatalf("reqs = %+v", deciders.reqs)
	}
	req := deciders.reqs[1]
	if req.Principal.Type != authz.EntityServiceAccount || req.Resource.ID != "ep_1" || req.Context.Path != "/api" || req.Context.Method != http.MethodPost || req.Context.AuthMethod != authz.AuthMethodClientCredentials {
		t.Fatalf("req = %+v", req)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("invalid decision request: %v", err)
	}
	if deciders.scopes[0].OrgID != "org_1" || deciders.scopes[0].TokenSessionID == "" {
		t.Fatalf("scope = %+v", deciders.scopes[0])
	}
}

func TestIdentityGateFailsClosedWithoutDeciders(t *testing.T) {
	ing := &Ingress{log: discardLogger()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	if _, ok := ing.applyIdentityGate(w, r, identityEndpoint(store.EndpointIdentity{}), "/"); ok || w.Code != http.StatusServiceUnavailable {
		t.Fatalf("ok=%v code=%d", ok, w.Code)
	}
}

func TestUpstreamHygiene(t *testing.T) {
	svc := newIdentityService(t)
	tok := issueSAToken(t, svc, "org_1")
	var got http.Header
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		http.SetCookie(w, &http.Cookie{Name: "mm_ep_session", Value: "forged"})
		http.SetCookie(w, &http.Cookie{Name: "app", Value: "ok"})
	})
	ep := identityEndpoint(store.EndpointIdentity{AuthorizationHeader: true})
	ing := &Ingress{log: discardLogger(), conns: nopConns{}}
	ing.proxy = ing.newProxy(0)
	r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	r.Header.Set("X-Mishmesh-Auth", "Bearer "+tok)
	r.Header.Set("X-Mishmesh-Principal", "spoofed")
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("X-Forwarded-Port", "1")
	r.Header.Set("X-Forwarded-Prefix", "/evil")
	r.Header.Set("X-Real-Ip", "1.2.3.4")
	r.Header.Set("Forwarded", "for=1.2.3.4")
	r.Header.Set("Cookie", "mm_oidc=a; mm_ep_session=b; app=c")
	w := httptest.NewRecorder()
	ing.proxyHTTP(w, r, &fakeAgentConn{backend: backend}, ep, "/", 0)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	for _, name := range []string{"X-Mishmesh-Auth", "X-Mishmesh-Principal", "Authorization", "X-Forwarded-Port", "X-Forwarded-Prefix", "X-Real-Ip", "Forwarded"} {
		if got.Get(name) != "" {
			t.Errorf("%s leaked upstream: %q", name, got.Get(name))
		}
	}
	if got.Get("Cookie") != "app=c" {
		t.Errorf("cookie = %q", got.Get("Cookie"))
	}
	if got.Get("X-Forwarded-For") == "" || got.Get("X-Forwarded-Proto") != "http" {
		t.Errorf("forwarded headers not re-set: %v", got)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "app" {
		t.Errorf("response cookies = %v", cookies)
	}
}

func TestUpstreamKeepsForeignAuthorization(t *testing.T) {
	h := http.Header{"Authorization": {"Bearer upstream-api-key"}}
	sanitizeUpstreamRequest(h, identityEndpoint(store.EndpointIdentity{AuthorizationHeader: true}))
	if h.Get("Authorization") != "Bearer upstream-api-key" {
		t.Fatalf("non-mishmesh authorization dropped: %v", h)
	}
}

func TestDropH2CUpgrade(t *testing.T) {
	h := http.Header{"Upgrade": {"h2c"}, "Http2-Settings": {"AAA"}, "Connection": {"Upgrade, HTTP2-Settings"}}
	dropH2CUpgrade(h)
	r := &http.Request{Header: h}
	if isUpgrade(r) || h.Get("Http2-Settings") != "" {
		t.Fatalf("h2c upgrade kept: %v", h)
	}
}
