package ingress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type lookupCounter struct {
	store.DataStore
	calls atomic.Int64
}

func (c *lookupCounter) GetEndpointBySubdomain(ctx context.Context, sub string) (*store.Endpoint, error) {
	c.calls.Add(1)
	return c.DataStore.GetEndpointBySubdomain(ctx, sub)
}

func (c *lookupCounter) GetEndpointByDomain(ctx context.Context, domain string) (*store.Endpoint, error) {
	c.calls.Add(1)
	return c.DataStore.GetEndpointByDomain(ctx, domain)
}

func (c *lookupCounter) GetEndpointByAccessHostname(ctx context.Context, host string) (*store.Endpoint, error) {
	c.calls.Add(1)
	return c.DataStore.GetEndpointByAccessHostname(ctx, host)
}

func newCountedIngress(t *testing.T, ttl time.Duration) (*Ingress, *lookupCounter) {
	t.Helper()
	base, _ := newCachedIngress(t, ttl)
	counter := &lookupCounter{DataStore: base.data}
	base.data = counter
	return base, counter
}

func serveHost(ing *Ingress, host string) int {
	rec := httptest.NewRecorder()
	ing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil))
	return rec.Code
}

func TestUnknownHostsAreNegativelyCached(t *testing.T) {
	ing, counter := newCountedIngress(t, time.Minute)
	const requests = 100
	for i := 0; i < requests; i++ {
		if code := serveHost(ing, "ghost.example.com"); code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", code)
		}
	}
	if got := counter.calls.Load(); got > 3 {
		t.Fatalf("store lookups for %d unknown-host requests = %d, want at most 3", requests, got)
	}
}

func TestNegativeCacheClearedByInvalidateCaches(t *testing.T) {
	ing, counter := newCountedIngress(t, time.Minute)
	serveHost(ing, "ghost.example.com")
	before := counter.calls.Load()
	serveHost(ing, "ghost.example.com")
	if counter.calls.Load() != before {
		t.Fatal("second request should have been served from the negative cache")
	}
	ing.InvalidateCaches()
	serveHost(ing, "ghost.example.com")
	if counter.calls.Load() == before {
		t.Fatal("InvalidateCaches did not clear the negative cache")
	}
}

func TestNegativeCacheExpiresQuickly(t *testing.T) {
	ing, counter := newCountedIngress(t, time.Minute)
	now := time.Now()
	ing.misses.now = func() time.Time { return now }
	serveHost(ing, "ghost.example.com")
	before := counter.calls.Load()
	now = now.Add(negativeCacheTTL + time.Millisecond)
	serveHost(ing, "ghost.example.com")
	if counter.calls.Load() == before {
		t.Fatal("negative entry outlived its ttl")
	}
}

func TestNegativeCacheDisabledWithZeroTTL(t *testing.T) {
	ing, counter := newCountedIngress(t, 0)
	for i := 0; i < 5; i++ {
		serveHost(ing, "ghost.example.com")
	}
	if got := counter.calls.Load(); got < 5 {
		t.Fatalf("lookups = %d, want every request to hit the store", got)
	}
}
