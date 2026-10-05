package ingress

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const maxCacheEntries = 20000

type ttlCache[V any] struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.RWMutex
	entries map[string]cacheEntry[V]
}

type cacheEntry[V any] struct {
	value   V
	expires time.Time
}

func newTTLCache[V any](ttl time.Duration) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, now: time.Now, entries: make(map[string]cacheEntry[V])}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	var zero V
	if c == nil || c.ttl <= 0 {
		return zero, false
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || !c.now().Before(e.expires) {
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[V]) put(key string, v V) {
	if c == nil || c.ttl <= 0 {
		return
	}
	now := c.now()
	c.mu.Lock()
	if len(c.entries) >= maxCacheEntries {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxCacheEntries {
			c.entries = make(map[string]cacheEntry[V])
		}
	}
	c.entries[key] = cacheEntry[V]{value: v, expires: now.Add(c.ttl)}
	c.mu.Unlock()
}

func (c *ttlCache[V]) invalidate(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *ttlCache[V]) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry[V])
	c.mu.Unlock()
}

func cached[V any](c *ttlCache[V], key string, load func() (V, error)) (V, error) {
	if v, ok := c.get(key); ok {
		return v, nil
	}
	v, err := load()
	if err != nil {
		var zero V
		return zero, err
	}
	c.put(key, v)
	return v, nil
}

func (i *Ingress) InvalidateCaches() {
	i.endpoints.clear()
	i.quotas.clear()
}

func (i *Ingress) endpointBySubdomain(ctx context.Context, sub string) (*store.Endpoint, error) {
	return cached(i.endpoints, "s:"+sub, func() (*store.Endpoint, error) { return i.data.GetEndpointBySubdomain(ctx, sub) })
}

func (i *Ingress) endpointByDomain(ctx context.Context, domain string) (*store.Endpoint, error) {
	return cached(i.endpoints, "d:"+domain, func() (*store.Endpoint, error) { return i.data.GetEndpointByDomain(ctx, domain) })
}

func (i *Ingress) endpointByID(ctx context.Context, id string) (*store.Endpoint, error) {
	return cached(i.endpoints, "i:"+id, func() (*store.Endpoint, error) { return i.data.GetEndpoint(ctx, id) })
}

func (i *Ingress) quotaFor(ctx context.Context, orgID string) (*store.Quota, error) {
	return cached(i.quotas, orgID, func() (*store.Quota, error) {
		q, err := i.data.GetQuota(ctx, orgID)
		if errors.Is(err, store.ErrNotFound) {
			return &store.Quota{}, nil
		}
		return q, err
	})
}
