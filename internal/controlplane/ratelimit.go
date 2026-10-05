package controlplane

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	limiterMaxKeys = 10000
	ipBurst        = 20
	ipRefill       = 3 * time.Second
	emailBurst     = 5
	emailRefill    = 12 * time.Second
)

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*bucket), now: time.Now}
}

func (l *rateLimiter) allow(key string, burst int, refill time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= limiterMaxKeys {
			l.evictStale(now)
		}
		b = &bucket{tokens: float64(burst), last: now}
		l.buckets[key] = b
	}
	b.tokens += float64(now.Sub(b.last)) / float64(refill)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *rateLimiter) evictStale(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) > time.Minute {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= limiterMaxKeys {
		l.buckets = make(map[string]*bucket)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *API) authRateAllowed(r *http.Request, email string) bool {
	if !a.limiter.allow("ip:"+clientIP(r), ipBurst, ipRefill) {
		return false
	}
	if email == "" {
		return true
	}
	return a.limiter.allow("email:"+email, emailBurst, emailRefill)
}
