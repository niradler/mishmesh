package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

const ownerOnlyCedar = `@id("owners") permit (principal in Role::"owner", action, resource);`

func TestPutPolicyAcceptsExactlyOneSource(t *testing.T) {
	f := newHierarchyFixture(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"neither", `{}`, http.StatusBadRequest},
		{"both", `{"matrix":{"owner":["policy:write"]},"cedar_src":"` + ownerOnlyCedar + `"}`, http.StatusBadRequest},
		{"parse error", `{"cedar_src":"permit ("}`, http.StatusBadRequest},
		{"unknown action", `{"cedar_src":"permit (principal, action == Action::\"bogus:act\", resource);"}`, http.StatusBadRequest},
		{"unknown attribute", `{"cedar_src":"permit (principal, action == Action::\"http:request\", resource) when { context.nope == 1 };"}`, http.StatusBadRequest},
		{"too large", `{"cedar_src":"` + strings.Repeat(" ", authz.MaxPolicyBytes+1) + `"}`, http.StatusBadRequest},
		{"valid cedar", `{"cedar_src":"` + strings.ReplaceAll(ownerOnlyCedar, `"`, `\"`) + `"}`, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc(t, f.owner, f.srv, http.MethodPut, "/api/v1/policy", tc.body, tc.want, nil)
		})
	}
	var got policyDTO
	doc(t, f.owner, f.srv, http.MethodGet, "/api/v1/policy", "", http.StatusOK, &got)
	if got.IsDefault || got.CedarSrc != ownerOnlyCedar {
		t.Fatalf("policy = default:%v src:%q", got.IsDefault, got.CedarSrc)
	}
	doc(t, f.admin, f.srv, http.MethodGet, "/api/v1/members", "", http.StatusForbidden, nil)
}

func TestPutPolicyMatrixIncludesDataPlane(t *testing.T) {
	f := newHierarchyFixture(t)
	doc(t, f.owner, f.srv, http.MethodPut, "/api/v1/policy", `{"matrix":{"owner":["policy:read","policy:write"]}}`, http.StatusOK, nil)
	var got policyDTO
	doc(t, f.owner, f.srv, http.MethodGet, "/api/v1/policy", "", http.StatusOK, &got)
	for _, act := range []authz.Action{authz.ActionEndpointAccess, authz.ActionHTTPRequest, authz.ActionAgentConnect} {
		if !strings.Contains(got.CedarSrc, string(act)) {
			t.Errorf("compiled matrix policy lacks data-plane action %s", act)
		}
	}
}

func TestDefaultPolicyIncludesDataPlane(t *testing.T) {
	f := newHierarchyFixture(t)
	var got policyDTO
	doc(t, f.owner, f.srv, http.MethodGet, "/api/v1/policy", "", http.StatusOK, &got)
	if !got.IsDefault || !strings.Contains(got.CedarSrc, string(authz.ActionHTTPRequest)) {
		t.Fatalf("default policy = %+v", got.IsDefault)
	}
}

func TestAuthorizerForOrgPolicy(t *testing.T) {
	tests := []struct {
		name         string
		src          string
		ownerAllowed bool
	}{
		{"custom policy applies", ownerOnlyCedar, true},
		{"corrupt stored policy fails closed", "permit (", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := newTestAPI(t)
			ctx := context.Background()
			if err := api.data.CreateOrg(ctx, &store.Org{ID: "org_x", Name: "x", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := api.data.SetOrgPolicy(ctx, &store.OrgPolicy{OrgID: "org_x", CedarSrc: tc.src, UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			az := api.authorizerFor(ctx, "org_x")
			if got := az.Decide(authz.DecisionRequest{}, authz.Facts{}).Owner; got != "org_x" {
				t.Fatalf("owner = %q, want org_x", got)
			}
			owner := authz.Principal{ID: "u", Role: authz.RoleOwner, Org: "org_x"}
			if got := az.Authorize(owner, authz.ActionMemberRead); got != tc.ownerAllowed {
				t.Fatalf("owner member:read = %v, want %v", got, tc.ownerAllowed)
			}
		})
	}
}

func TestControlPlaneOnlyPolicyGetsDataPlaneDefaults(t *testing.T) {
	api, _ := newTestAPI(t)
	ctx := context.Background()
	if err := api.data.CreateOrg(ctx, &store.Org{ID: "org_x", Name: "x", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := api.data.SetOrgPolicy(ctx, &store.OrgPolicy{OrgID: "org_x", CedarSrc: `permit (principal in Role::"owner", action == Action::"agent:read", resource);`, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	az := api.authorizerFor(ctx, "org_x")
	req := authz.DecisionRequest{
		Principal: authz.EntityRef{Type: authz.EntityAnonymous, ID: authz.AnonymousID},
		Action:    authz.ActionHTTPRequest,
		Resource:  authz.EntityRef{Type: authz.EntityEndpoint, ID: "ep_1"},
		Context:   authz.RequestContext{ClientIP: "203.0.113.7", Host: "a.example.com", Method: "GET", Path: "/", Protocol: "http", AuthMethod: authz.AuthMethodNone},
	}
	facts := authz.Facts{
		Subject:  &authz.Subject{Ref: req.Principal},
		Endpoint: &authz.EndpointEntity{ID: "ep_1", Kind: "http"},
	}
	if d := az.Decide(req, facts); !d.Allowed {
		t.Fatalf("custom control-plane policy dropped data-plane defaults: %+v", d)
	}
}

type policyFaultStore struct {
	store.DataStore
}

func (policyFaultStore) GetOrgPolicy(context.Context, string) (*store.OrgPolicy, error) {
	return nil, errors.New("db down")
}

func TestAuthorizerFailsClosedOnStoreError(t *testing.T) {
	inner, err := sqlite.Open(filepath.Join(t.TempDir(), "fault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	api := New(policyFaultStore{inner}, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	az := api.authorizerFor(context.Background(), "org_x")
	if az.Authorize(authz.Principal{ID: "u", Role: authz.RoleOwner, Org: "org_x"}, authz.ActionAgentRead) {
		t.Fatal("store error must not fall back to the permissive default policy")
	}
	if _, cached := api.authzCache["org_x"]; cached {
		t.Fatal("transient store errors must not be cached")
	}
}
