package ingress

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestDecisionPathIsCanonical(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"/admin", "/admin"},
		{"/a/../admin", "/admin"},
		{"//admin", "/admin"},
		{"/a/%2e%2e/admin", "/admin"},
		{"/%2e%2e/admin", "/admin"},
		{"/./admin", "/admin"},
		{"/admin/", "/admin/"},
		{"/a/..//admin/./x", "/admin/x"},
		{"/", "/"},
		{"/a/..", "/"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			deciders := &fakeDeciders{decide: allowAll}
			ing := &Ingress{log: discardLogger(), deciders: deciders}
			r := httptest.NewRequest(http.MethodGet, "http://demo.localhost"+tc.target, nil)
			if _, err := ing.decideEndpoint(r, identityEndpoint(store.EndpointIdentity{}), r.URL.Path, nil); err != nil {
				t.Fatal(err)
			}
			for _, req := range deciders.reqs {
				if req.Action == authz.ActionHTTPRequest && req.Context.Path != tc.want {
					t.Fatalf("decision path = %q, want %q", req.Context.Path, tc.want)
				}
			}
			if len(deciders.reqs) == 0 {
				t.Fatal("no decision requested")
			}
		})
	}
}
