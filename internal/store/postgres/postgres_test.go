package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MISHMESH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MISHMESH_TEST_POSTGRES_DSN not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOrgAgentTokenRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Nanosecond)

	org := &store.Org{ID: "org_pg1", Name: "acme", CreatedAt: now}
	if err := s.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	ag := &store.Agent{ID: "ag_pg1", OrgID: org.ID, Name: "a", Status: store.AgentActive, CreatedAt: now}
	if err := s.CreateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	raw, hash, err := store.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, &store.Token{ID: "tok_pg1", OrgID: org.ID, AgentID: ag.ID, Hash: hash, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetTokenByHash(ctx, store.HashToken(raw))
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if got.AgentID != ag.ID {
		t.Fatalf("agent id: got %q want %q", got.AgentID, ag.ID)
	}

	if err := s.RevokeToken(ctx, "tok_pg1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTokenByHash(ctx, store.HashToken(raw)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked token lookup: want ErrNotFound, got %v", err)
	}
}

func TestEndpointSubdomainLookupAndCleanup(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	mustSeed(t, ctx, s, now)

	ep := &store.Endpoint{
		ID: "ep_pg1", AgentID: "ag_pg2", OrgID: "org_pg2",
		Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral,
		Subdomain: "demo-pg", CreatedAt: now,
	}
	if err := s.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetEndpointBySubdomain(ctx, "demo-pg")
	if err != nil {
		t.Fatalf("by subdomain: %v", err)
	}
	if got.ID != "ep_pg1" {
		t.Fatalf("got %q", got.ID)
	}

	if _, err := s.GetEndpointBySubdomain(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing subdomain: want ErrNotFound, got %v", err)
	}

	if err := s.DeleteEndpoint(ctx, "ep_pg1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEndpoint(ctx, "ep_pg1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted endpoint: want ErrNotFound, got %v", err)
	}
}

func mustSeed(t *testing.T, ctx context.Context, s *Store, now time.Time) {
	t.Helper()
	if err := s.CreateOrg(ctx, &store.Org{ID: "org_pg2", Name: "o", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgent(ctx, &store.Agent{ID: "ag_pg2", OrgID: "org_pg2", Name: "a", Status: store.AgentActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}
