package ratelimit

import (
	"context"
	"sync"
	"time"
)

const defaultMaxKeys = 100000

type bucket struct {
	tokens float64
	last   time.Time
}

type Memory struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	maxKeys int
	now     func() time.Time
}

var _ Limiter = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{buckets: make(map[string]*bucket), maxKeys: defaultMaxKeys, now: time.Now}
}

func (m *Memory) Allow(_ context.Context, key string, limit Limit) Decision {
	if !limit.Valid() {
		return Decision{Allowed: true}
	}
	capacity := float64(limit.Capacity())
	perToken := float64(limit.Period) / float64(limit.Requests)

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	b, ok := m.buckets[key]
	if !ok {
		if len(m.buckets) >= m.maxKeys {
			m.evict(now, capacity, perToken)
		}
		b = &bucket{tokens: capacity, last: now}
		m.buckets[key] = b
	}
	b.tokens += float64(now.Sub(b.last)) / perToken
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.last = now
	if b.tokens < 1 {
		return Decision{RetryAfter: time.Duration((1 - b.tokens) * perToken)}
	}
	b.tokens--
	return Decision{Allowed: true}
}

func (m *Memory) evict(now time.Time, capacity, perToken float64) {
	for k, b := range m.buckets {
		if b.tokens+float64(now.Sub(b.last))/perToken >= capacity {
			delete(m.buckets, k)
		}
	}
	if len(m.buckets) >= m.maxKeys {
		m.buckets = make(map[string]*bucket)
	}
}
