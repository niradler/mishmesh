package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
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

	org := &store.Org{ID: "org_1", Name: "acme", CreatedAt: now}
	if err := s.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	ag := &store.Agent{ID: "ag_1", OrgID: org.ID, Name: "a", Status: store.AgentActive, CreatedAt: now}
	if err := s.CreateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	raw, hash, err := store.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, &store.Token{ID: "tok_1", OrgID: org.ID, AgentID: ag.ID, Hash: hash, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetTokenByHash(ctx, store.HashToken(raw))
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if got.AgentID != ag.ID {
		t.Fatalf("agent id: got %q want %q", got.AgentID, ag.ID)
	}

	if err := s.RevokeToken(ctx, "tok_1"); err != nil {
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

	ep := &store.Endpoint{ID: "ep_1", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral, Subdomain: "demo", CreatedAt: now}
	if err := s.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetEndpointBySubdomain(ctx, "demo")
	if err != nil {
		t.Fatalf("by subdomain: %v", err)
	}
	if got.ID != "ep_1" {
		t.Fatalf("got %q", got.ID)
	}

	if _, err := s.GetEndpointBySubdomain(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing subdomain: want ErrNotFound, got %v", err)
	}

	if err := s.DeleteEndpoint(ctx, "ep_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEndpoint(ctx, "ep_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted endpoint: want ErrNotFound, got %v", err)
	}
}

func TestEndpointPolicyAndDomainRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	mustSeed(t, ctx, s, now)

	ep := &store.Endpoint{
		ID: "ep_pol", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP,
		Lifecycle: store.LifecycleReserved, Domain: "app.example.com", CreatedAt: now,
		Policy: &store.EndpointPolicy{
			RequestHeadersAdd: map[string]string{"X-Tunnel": "mishmesh"},
			StripPathPrefix:   "/api",
			BasicAuthUser:     "alice",
			IPAllow:           []string{"10.0.0.0/8"},
			ForceHTTPS:        true,
		},
	}
	if err := s.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEndpointByDomain(ctx, "app.example.com")
	if err != nil {
		t.Fatalf("by domain: %v", err)
	}
	if got.Policy == nil || got.Policy.RequestHeadersAdd["X-Tunnel"] != "mishmesh" || !got.Policy.ForceHTTPS {
		t.Fatalf("policy roundtrip mismatch: %+v", got.Policy)
	}
	got.Policy.StripPathPrefix = "/v2"
	if err := s.UpdateEndpoint(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetEndpoint(ctx, "ep_pol")
	if again.Policy.StripPathPrefix != "/v2" {
		t.Fatalf("update policy: got %q", again.Policy.StripPathPrefix)
	}
	n, err := s.CountEndpoints(ctx, "org_1")
	if err != nil || n != 1 {
		t.Fatalf("count endpoints: n=%d err=%v", n, err)
	}
}

func TestQuotaUserMembershipSession(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	mustSeed(t, ctx, s, now)

	if err := s.SetQuota(ctx, &store.Quota{OrgID: "org_1", MaxAgents: 5, MaxEndpoints: 10, MaxBandwidthBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuota(ctx, &store.Quota{OrgID: "org_1", MaxAgents: 7}); err != nil {
		t.Fatal(err)
	}
	q, err := s.GetQuota(ctx, "org_1")
	if err != nil || q.MaxAgents != 7 {
		t.Fatalf("quota upsert: %+v err=%v", q, err)
	}

	u := &store.User{ID: "usr_1", Email: "Alice@Example.com", Name: "Alice", PasswordHash: "h", GoogleSub: "g-123", CreatedAt: now}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	byEmail, err := s.GetUserByEmail(ctx, "alice@example.com")
	if err != nil || byEmail.ID != "usr_1" {
		t.Fatalf("get user by email: %+v err=%v", byEmail, err)
	}
	bySub, err := s.GetUserByGoogleSub(ctx, "g-123")
	if err != nil || bySub.ID != "usr_1" {
		t.Fatalf("get user by sub: %+v err=%v", bySub, err)
	}

	if err := s.CreateMembership(ctx, &store.Membership{OrgID: "org_1", UserID: "usr_1", Role: store.RoleOwner, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mem, err := s.GetMembership(ctx, "org_1", "usr_1")
	if err != nil || mem.Role != store.RoleOwner {
		t.Fatalf("membership: %+v err=%v", mem, err)
	}

	sess := &store.Session{IDHash: "sh_1", UserID: "usr_1", OrgID: "org_1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "sh_1"); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if err := s.DeleteExpiredSessions(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "sh_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session: want ErrNotFound, got %v", err)
	}
}

func mustSeed(t *testing.T, ctx context.Context, s *Store, now time.Time) {
	t.Helper()
	if err := s.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}
