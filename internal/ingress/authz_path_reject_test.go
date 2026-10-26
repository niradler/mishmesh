package ingress

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestIdentityGateRejectsPathsTheDecisionDidNotEvaluate(t *testing.T) {
	denyAdmin := func(req authz.DecisionRequest) (authz.Decision, error) {
		if req.Context.Path == "/admin" || strings.HasPrefix(req.Context.Path, "/admin/") {
			return authz.Decision{Reason: authz.ReasonPolicyDeny}, nil
		}
		return authz.Decision{Allowed: true, Reason: authz.ReasonAllow}, nil
	}
	targets := []string{
		"/a/../admin",
		"//admin",
		"/%2e%2e/admin",
		"/a%2fadmin",
		"/admin/.",
		"/admin/",
		"/admin;x",
		`/a\..\admin`,
		"/admin%2f",
		"/./admin",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			deciders := &fakeDeciders{decide: denyAdmin}
			ing := &Ingress{log: discardLogger(), identity: newIdentityService(t), deciders: deciders}
			r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
			r.URL.Path, r.URL.RawPath = parsedPath(t, target)
			w := httptest.NewRecorder()
			if _, ok := ing.applyIdentityGate(w, r, identityEndpoint(store.EndpointIdentity{}), r.URL.Path); ok {
				t.Fatalf("%q reached upstream", target)
			}
			for _, req := range deciders.reqs {
				if req.Context.Path != r.URL.Path {
					t.Fatalf("decision evaluated %q but upstream would serve %q", req.Context.Path, r.URL.Path)
				}
			}
		})
	}
}

func TestIdentityGateAllowsPlainPaths(t *testing.T) {
	for _, target := range []string{"/", "/api/v1/items", "/files/a%20b", "/dir/"} {
		t.Run(target, func(t *testing.T) {
			deciders := &fakeDeciders{decide: allowAll}
			ing := &Ingress{log: discardLogger(), identity: newIdentityService(t), deciders: deciders}
			r := httptest.NewRequest(http.MethodGet, "http://demo.localhost"+target, nil)
			if _, ok := ing.applyIdentityGate(httptest.NewRecorder(), r, identityEndpoint(store.EndpointIdentity{}), r.URL.Path); !ok {
				t.Fatalf("%q rejected", target)
			}
		})
	}
}

func parsedPath(t *testing.T, target string) (string, string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path, u.RawPath
}
