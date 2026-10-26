package sshfwd

import (
	"net"
	"sync"
	"time"
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

func (t *failureThrottle) blocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.entries[ip]
	if !ok {
		return false
	}
	if t.now().Sub(w.start) >= t.window {
		delete(t.entries, ip)
		return false
	}
	return w.count >= t.limit
}

func (t *failureThrottle) fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if len(t.entries) >= 4096 {
		for k, w := range t.entries {
			if now.Sub(w.start) >= t.window {
				delete(t.entries, k)
			}
		}
	}
	w, ok := t.entries[ip]
	if !ok || now.Sub(w.start) >= t.window {
		t.entries[ip] = &failureWindow{count: 1, start: now}
		return
	}
	w.count++
}

func (t *failureThrottle) reset(ip string) {
	t.mu.Lock()
	delete(t.entries, ip)
	t.mu.Unlock()
}

func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
