package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestAuthzCacheExpiresSoPeerNodePolicyChangesApply(t *testing.T) {
	api, _ := newTestAPI(t)
	ctx := context.Background()
	if err := api.data.CreateOrg(ctx, &store.Org{ID: "org_x", Name: "x", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	api.now = func() time.Time { return clock }

	before := api.authorizerFor(ctx, "org_x")
	if before != api.defaultAuthz {
		t.Fatal("expected default authorizer before any policy exists")
	}

	src := authz.CompileMatrix(map[string][]authz.Action{"owner": {"agent:read"}})
	if err := api.data.SetOrgPolicy(ctx, &store.OrgPolicy{OrgID: "org_x", CedarSrc: src, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(authzCacheTTL - time.Second)
	if api.authorizerFor(ctx, "org_x") != before {
		t.Fatal("authorizer should still be cached inside the TTL")
	}

	clock = clock.Add(2 * time.Second)
	after := api.authorizerFor(ctx, "org_x")
	if after == before {
		t.Fatal("authorizer must be reloaded once the TTL elapses, so a policy written on another node takes effect")
	}
}

func TestAuthzCacheTTLIsAtMostFiveSeconds(t *testing.T) {
	if authzCacheTTL > 5*time.Second {
		t.Fatalf("authz cache ttl %v exceeds the 5s staleness bound", authzCacheTTL)
	}
}
