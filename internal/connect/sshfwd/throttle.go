package sshfwd

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	ipFailureMultiplier = 5
	maxThrottleEntries  = 4096
)

type failureWindow struct {
	count int
	start time.Time
}

type failureThrottle struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*failureWindow
}

func newFailureThrottle(limit int, window time.Duration) *failureThrottle {
	return &failureThrottle{limit: limit, window: window, now: time.Now, entries: make(map[string]*failureWindow)}
}

func identityKey(ip, user string) string { return "u|" + ip + "|" + user }

func ipKey(ip string) string { return "i|" + ip }

func (t *failureThrottle) over(key string, limit int) bool {
	w, ok := t.entries[key]
	if !ok {
		return false
	}
	if t.now().Sub(w.start) >= t.window {
		delete(t.entries, key)
		return false
	}
	return w.count >= limit
}

func (t *failureThrottle) blockedIP(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.over(ipKey(ip), t.limit*ipFailureMultiplier)
}

func (t *failureThrottle) blocked(ip, user string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.over(identityKey(ip, user), t.limit) || t.over(ipKey(ip), t.limit*ipFailureMultiplier)
}

func (t *failureThrottle) fail(ip, user string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if len(t.entries) >= maxThrottleEntries {
		for k, w := range t.entries {
			if now.Sub(w.start) >= t.window {
				delete(t.entries, k)
			}
		}
	}
	t.bump(identityKey(ip, user), now)
	t.bump(ipKey(ip), now)
}

func (t *failureThrottle) bump(key string, now time.Time) {
	w, ok := t.entries[key]
	if !ok || now.Sub(w.start) >= t.window {
		t.entries[key] = &failureWindow{count: 1, start: now}
		return
	}
	w.count++
}

func remoteIP(addr net.Addr) (string, bool) {
	if addr == nil {
		return "", false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", false
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		return "", false
	}
	return parsed.Unmap().WithZone("").String(), true
}
