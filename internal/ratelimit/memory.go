package ratelimit

import (
	"context"
	"hash/maphash"
	"sync"
	"time"
)

const (
	defaultMaxKeys = 100000
	shardCount     = 64
	evictSamples   = 16
	sweepInterval  = time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
	fullAt time.Time
}

type shard struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type Memory struct {
	shards      [shardCount]shard
	seed        maphash.Seed
	maxPerShard int
	now         func() time.Time
}

var _ Limiter = (*Memory)(nil)

func NewMemory() *Memory {
	m := &Memory{seed: maphash.MakeSeed(), now: time.Now}
	m.setMaxKeys(defaultMaxKeys)
	for i := range m.shards {
		m.shards[i].buckets = make(map[string]*bucket)
	}
	return m
}

func (m *Memory) setMaxKeys(maxKeys int) {
	m.maxPerShard = max(1, maxKeys/shardCount)
}

func (m *Memory) Allow(_ context.Context, key string, limit Limit) Decision {
	if !limit.Valid() {
		return Decision{Allowed: true}
	}
	capacity := float64(limit.Capacity())
	perToken := float64(limit.Period) / float64(limit.Requests)

	s := &m.shards[maphash.String(m.seed, key)%shardCount]
	s.mu.Lock()
	defer s.mu.Unlock()
	now := m.now()
	if now.Sub(s.lastSweep) >= sweepInterval {
		s.sweep(now)
	}
	b, ok := s.buckets[key]
	if !ok {
		if len(s.buckets) >= m.maxPerShard {
			s.evict(now)
		}
		b = &bucket{tokens: capacity, last: now}
		s.buckets[key] = b
	}
	b.tokens += float64(now.Sub(b.last)) / perToken
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.last = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	b.fullAt = now.Add(time.Duration((capacity - b.tokens) * perToken))
	if !allowed {
		return Decision{RetryAfter: time.Duration((1 - b.tokens) * perToken)}
	}
	return Decision{Allowed: true}
}

func (s *shard) sweep(now time.Time) {
	s.lastSweep = now
	for k, b := range s.buckets {
		if !b.fullAt.After(now) {
			delete(s.buckets, k)
		}
	}
}

func (s *shard) evict(now time.Time) {
	var victim string
	var victimAt time.Time
	picked := false
	sampled := 0
	for k, b := range s.buckets {
		if !b.fullAt.After(now) {
			delete(s.buckets, k)
			return
		}
		if !picked || b.fullAt.Before(victimAt) {
			victim, victimAt, picked = k, b.fullAt, true
		}
		sampled++
		if sampled >= evictSamples {
			break
		}
	}
	if picked {
		delete(s.buckets, victim)
	}
}

func (m *Memory) size() int {
	total := 0
	for i := range m.shards {
		s := &m.shards[i]
		s.mu.Lock()
		total += len(s.buckets)
		s.mu.Unlock()
	}
	return total
}
