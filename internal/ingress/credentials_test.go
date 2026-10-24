package ingress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

type fakeSecrets map[string]string

func (f fakeSecrets) Open(_ context.Context, orgID, name string) ([]byte, error) {
	v, ok := f[orgID+"/"+name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return []byte(v), nil
}

func credentialEndpoint() *store.Endpoint {
	return identityEndpoint(store.EndpointIdentity{Credentials: map[string]store.EndpointCredential{
		"ops": {Header: "X-Api-Key", Secret: "ops-key"},
		"dev": {Header: "Authorization", Secret: "dev-key", Prefix: "Bearer "},
	}})
}

func annotated(credentials ...string) func(authz.DecisionRequest) (authz.Decision, error) {
	return func(authz.DecisionRequest) (authz.Decision, error) {
		d := authz.Decision{Allowed: true, Reason: authz.ReasonAllow}
		for i, c := range credentials {
			d.Policies = append(d.Policies, authz.DeterminingPolicy{ID: "p" + string(rune('0'+i)), Annotations: map[string]string{authz.CredentialAnnotation: c}})
		}
		return d, nil
	}
}

func TestCredentialInjection(t *testing.T) {
	secrets := fakeSecrets{"org_1/ops-key": "ops-secret", "org_1/dev-key": "dev-secret", "org_2/ops-key": "foreign"}
	tests := []struct {
		name       string
		decide     func(authz.DecisionRequest) (authz.Decision, error)
		wantHeader string
		wantValue  string
	}{
		{"ops group gets api key", annotated("ops"), "X-Api-Key", "ops-secret"},
		{"dev group gets bearer", annotated("dev"), "Authorization", "Bearer dev-secret"},
		{"same credential twice is not a conflict", annotated("ops", "ops"), "X-Api-Key", "ops-secret"},
		{"no annotation injects nothing", annotated(), "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newIdentityService(t)
			var got http.Header
			backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
			})
			ep := credentialEndpoint()
			ing := &Ingress{log: discardLogger(), conns: nopConns{}, identity: svc, deciders: &fakeDeciders{decide: tc.decide}, secrets: secrets}
			ing.proxy = ing.newProxy(0)
			r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/x", nil)
			r.Header.Set(identityAuthHeader, "Bearer "+issueSAToken(t, svc, "org_1"))
			r.Header.Set("X-Api-Key", "client-forged")
			r.Header.Set("Authorization", "Bearer client-forged")
			w := httptest.NewRecorder()
			r, ok := ing.applyIdentityGate(w, r, ep, "/x")
			if !ok {
				t.Fatalf("rejected: %d %s", w.Code, w.Body)
			}
			ing.proxyHTTP(w, r, &fakeAgentConn{backend: backend}, ep, "/x", 0)
			if w.Code != http.StatusOK {
				t.Fatalf("code = %d", w.Code)
			}
			for _, h := range []string{"X-Api-Key", "Authorization"} {
				want := ""
				if h == tc.wantHeader {
					want = tc.wantValue
				}
				if got.Get(h) != want {
					t.Errorf("%s = %q, want %q", h, got.Get(h), want)
				}
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Errorf("secret leaked in response: %q", w.Body.String())
			}
		})
	}
}

func TestCredentialInjectionFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		decide  func(authz.DecisionRequest) (authz.Decision, error)
		secrets SecretOpener
	}{
		{"conflicting credentials", annotated("ops", "dev"), fakeSecrets{"org_1/ops-key": "a", "org_1/dev-key": "b"}},
		{"unknown credential", annotated("nope"), fakeSecrets{"org_1/ops-key": "a"}},
		{"missing secret", annotated("ops"), fakeSecrets{}},
		{"secret from another org is not used", annotated("ops"), fakeSecrets{"org_2/ops-key": "foreign"}},
		{"no secret store", annotated("ops"), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newIdentityService(t)
			ing := &Ingress{log: discardLogger(), identity: svc, deciders: &fakeDeciders{decide: tc.decide}, secrets: tc.secrets}
			rec := &accessRecord{}
			r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/x", nil)
			r = r.WithContext(context.WithValue(r.Context(), accessKey{}, rec))
			r.Header.Set(identityAuthHeader, "Bearer "+issueSAToken(t, svc, "org_1"))
			w := httptest.NewRecorder()
			if _, ok := ing.applyIdentityGate(w, r, credentialEndpoint(), "/x"); ok || w.Code != http.StatusBadGateway {
				t.Fatalf("ok=%v code=%d, want 502", ok, w.Code)
			}
			if rec.decision == nil || rec.decision.Allowed || rec.decision.Reason != reasonCredentialUnavailable {
				t.Fatalf("recorded decision = %+v", rec.decision)
			}
			if strings.Contains(w.Body.String(), "foreign") {
				t.Fatalf("body leaked secret: %q", w.Body.String())
			}
		})
	}
}

func TestStaticHeadersCannotSetCredentialHeaders(t *testing.T) {
	h := http.Header{"X-Api-Key": {"static"}, "Authorization": {"Bearer static"}, "X-Other": {"kept"}}
	r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	applyUpstreamCredential(h, r, credentialEndpoint())
	if h.Get("X-Api-Key") != "" || h.Get("Authorization") != "" || h.Get("X-Other") != "kept" {
		t.Fatalf("headers = %v", h)
	}
}

func TestMergePolicies(t *testing.T) {
	in := []authz.DeterminingPolicy{{ID: "b"}, {ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "a"}}
	got := mergePolicies(in)
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
		t.Fatalf("merged = %+v", got)
	}
	if mergePolicies(nil) != nil {
		t.Fatal("nil input should stay nil")
	}
}
