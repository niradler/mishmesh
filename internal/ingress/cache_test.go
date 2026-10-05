package ingress

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type countingStore struct {
	store.DataStore
	endpointLookups atomic.Int64
	quotaLookups    atomic.Int64
}

func (c *countingStore) GetEndpointBySubdomain(ctx context.Context, sub string) (*store.Endpoint, error) {
	c.endpointLookups.Add(1)
	return c.DataStore.GetEndpointBySubdomain(ctx, sub)
}

func (c *countingStore) GetQuota(ctx context.Context, org string) (*store.Quota, error) {
	c.quotaLookups.Add(1)
	return c.DataStore.GetQuota(ctx, org)
}

func newCachedIngress(t *testing.T, ttl time.Duration) (*Ingress, *countingStore) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	if err := data.CreateEndpoint(ctx, &store.Endpoint{ID: "ep_1", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral, Subdomain: "app", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cs := &countingStore{DataStore: data}
	ing := New(Options{Data: cs, Conns: memory.NewConnStore(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseDomain: "example.com", LookupCacheTTL: ttl})
	return ing, cs
}

func serveOnce(ing *Ingress) int {
	rec := httptest.NewRecorder()
	ing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil))
	return rec.Code
}

func TestLookupCacheAvoidsRepeatedStoreHits(t *testing.T) {
	ing, cs := newCachedIngress(t, time.Minute)
	for i := 0; i < 50; i++ {
		if code := serveOnce(ing); code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (no agent)", code)
		}
	}
	if got := cs.endpointLookups.Load(); got != 1 {
		t.Fatalf("endpoint lookups = %d, want 1", got)
	}
	if got := cs.quotaLookups.Load(); got != 1 {
		t.Fatalf("quota lookups = %d, want 1", got)
	}
	ing.InvalidateCaches()
	serveOnce(ing)
	if got := cs.endpointLookups.Load(); got != 2 {
		t.Fatalf("endpoint lookups after invalidate = %d, want 2", got)
	}
}

func TestLookupCacheDisabledWithZeroTTL(t *testing.T) {
	ing, cs := newCachedIngress(t, 0)
	for i := 0; i < 5; i++ {
		serveOnce(ing)
	}
	if got := cs.endpointLookups.Load(); got != 5 {
		t.Fatalf("endpoint lookups = %d, want 5", got)
	}
}

func TestLookupCacheExpires(t *testing.T) {
	ing, cs := newCachedIngress(t, time.Minute)
	now := time.Now()
	ing.endpoints.now = func() time.Time { return now }
	serveOnce(ing)
	now = now.Add(2 * time.Minute)
	serveOnce(ing)
	if got := cs.endpointLookups.Load(); got != 2 {
		t.Fatalf("endpoint lookups = %d, want 2", got)
	}
}
