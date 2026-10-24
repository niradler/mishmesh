package authz

import (
	"crypto/sha256"
	"sync"
)

const DefaultCacheSize = 1024

type Cache struct {
	mu      sync.Mutex
	max     int
	entries map[[sha256.Size]byte]*Authorizer
}

func NewCache(max int) *Cache {
	if max <= 0 {
		max = DefaultCacheSize
	}
	return &Cache{max: max, entries: map[[sha256.Size]byte]*Authorizer{}}
}

func (c *Cache) Get(src []byte) (*Authorizer, error) {
	key := sha256.Sum256(src)
	c.mu.Lock()
	az, ok := c.entries[key]
	c.mu.Unlock()
	if ok {
		return az, nil
	}
	az, err := New(src)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[key]; ok {
		return existing, nil
	}
	if len(c.entries) >= c.max {
		clear(c.entries)
	}
	c.entries[key] = az
	return az, nil
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
