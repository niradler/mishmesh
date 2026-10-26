package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func newStore(t testing.TB) *Store {
	t.Helper()
	return newStoreWithPool(t, PoolConfig{})
}

func isolatedSchemaDSN(t testing.TB) string {
	t.Helper()
	baseDSN := os.Getenv("MISHMESH_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("MISHMESH_TEST_POSTGRES_DSN not set")
	}
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.ToLower(store.NewID("t"))
	if _, err := admin.Exec(`CREATE SCHEMA "` + name + `"`); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS "` + name + `" CASCADE`)
		_ = admin.Close()
	})
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func newStoreWithPool(t testing.TB, pool PoolConfig) *Store {
	t.Helper()
	return openStoreOn(t, isolatedSchemaDSN(t), pool)
}

func openStoreOn(t testing.TB, dsn string, pool PoolConfig) *Store {
	t.Helper()
	s, err := Open(dsn, pool)
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
