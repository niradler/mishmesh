package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newDomainStore(t *testing.T) store.DataStore {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "acme.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "org_a", Name: "a", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAcmeHostPolicyVerifiedCustomDomains(t *testing.T) {
	ctx := context.Background()
	data := newDomainStore(t)
	now := time.Now()
	verified := &store.Domain{ID: "dom_1", OrgID: "org_a", Name: "app.example.com", Token: "t1", VerifiedAt: &now, CreatedAt: now}
	pending := &store.Domain{ID: "dom_2", OrgID: "org_a", Name: "pending.example.com", Token: "t2", CreatedAt: now}
	for _, d := range []*store.Domain{verified, pending} {
		if err := data.CreateDomain(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	policy := acmeHostPolicy("mishmesh.io", customDomainLookup(config.Server{DomainVerification: true}, data))

	tests := []struct {
		host  string
		allow bool
	}{
		{"app.example.com", true},
		{"APP.example.com", true},
		{"x.mishmesh.io", true},
		{"pending.example.com", false},
		{"unknown.example.com", false},
		{"mishmesh.io.evil.com", false},
	}
	for _, tt := range tests {
		err := policy(ctx, tt.host)
		if (err == nil) != tt.allow {
			t.Errorf("host %q: allow=%v, got err=%v", tt.host, tt.allow, err)
		}
	}
}

func TestAcmeHostPolicyWithoutVerificationUsesEndpointDomains(t *testing.T) {
	ctx := context.Background()
	data := newDomainStore(t)
	if err := data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_a", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: "ep_1", AgentID: "ag_1", OrgID: "org_a", Kind: store.KindHTTP, Lifecycle: store.LifecycleReserved, Domain: "home.example.com", CreatedAt: time.Now()}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	policy := acmeHostPolicy("mishmesh.io", customDomainLookup(config.Server{DomainVerification: false}, data))
	if err := policy(ctx, "home.example.com"); err != nil {
		t.Errorf("endpoint domain should be allowed: %v", err)
	}
	if err := policy(ctx, "other.example.com"); err == nil {
		t.Error("unbound domain should be rejected")
	}
}

func TestCachedLookup(t *testing.T) {
	calls := 0
	allowed := true
	lookup := func(context.Context, string) (bool, error) {
		calls++
		return allowed, nil
	}
	clock := time.Now()
	c := newCachedLookup(lookup, time.Minute)
	c.now = func() time.Time { return clock }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if ok, err := c.allowed(ctx, "a.example.com"); err != nil || !ok {
			t.Fatalf("allowed: %v %v", ok, err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls within ttl = %d, want 1", calls)
	}

	allowed = false
	if ok, _ := c.allowed(ctx, "a.example.com"); !ok {
		t.Fatal("cached verdict should still apply within ttl")
	}
	clock = clock.Add(2 * time.Minute)
	if ok, _ := c.allowed(ctx, "a.example.com"); ok || calls != 2 {
		t.Fatalf("after ttl: allowed=%v calls=%d", ok, calls)
	}
}

func TestCachedLookupDoesNotCacheErrors(t *testing.T) {
	calls := 0
	lookup := func(context.Context, string) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("db down")
		}
		return true, nil
	}
	c := newCachedLookup(lookup, time.Minute)
	ctx := context.Background()
	if _, err := c.allowed(ctx, "a.example.com"); err == nil {
		t.Fatal("expected lookup error")
	}
	if ok, err := c.allowed(ctx, "a.example.com"); err != nil || !ok {
		t.Fatalf("retry after error: %v %v", ok, err)
	}
}
