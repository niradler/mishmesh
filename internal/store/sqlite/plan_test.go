package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, " | ")
}

func requireIndexedPlan(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	plan := queryPlan(t, s, query, args...)
	t.Logf("plan: %s", plan)
	if strings.Contains(plan, "SCAN") && !strings.Contains(plan, "SCAN CONSTANT ROW") {
		t.Fatalf("query scans a table: %s", plan)
	}
}

func TestEndpointDomainLookupUsesIndex(t *testing.T) {
	s := newStore(t)
	requireIndexedPlan(t, s, endpointByDomainQuery, "a.com")
}

func TestEndpointByDomainNormalizes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	mustSeed(t, ctx, s, now)
	for id, domain := range map[string]string{"ep_c": "Solo.example.com."} {
		ep := &store.Endpoint{ID: id, AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP, Lifecycle: store.LifecycleReserved, Domain: domain, CreatedAt: now}
		if err := s.CreateEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.GetEndpointByDomain(ctx, "other.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown domain: want ErrNotFound, got %v", err)
	}
	got, err := s.GetEndpointByDomain(ctx, "SOLO.example.com.")
	if err != nil || got.ID != "ep_c" {
		t.Fatalf("normalized lookup: got %v err %v", got, err)
	}
}
