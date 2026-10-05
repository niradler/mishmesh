package ingress

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/store"
)

func gateRequest(remote, xff string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func mustTrusted(t *testing.T, csv string) []*net.IPNet {
	t.Helper()
	nets, err := ParseTrustedProxies(csv)
	if err != nil {
		t.Fatal(err)
	}
	return nets
}

func rateLimitedEndpoint(id string, rl *store.RateLimit) *store.Endpoint {
	return &store.Endpoint{ID: id, Policy: &store.EndpointPolicy{RateLimit: rl}}
}

func TestRateLimitPerIP(t *testing.T) {
	ep := rateLimitedEndpoint("ep_1", &store.RateLimit{Requests: 1, PeriodSeconds: 60, Burst: 2})
	deps := gateDeps{limiter: ratelimit.NewMemory()}

	for i := 0; i < 2; i++ {
		if !applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.1:1000", ""), ep, deps) {
			t.Fatalf("request %d within burst blocked", i)
		}
	}
	w := httptest.NewRecorder()
	if applyPolicyGate(w, gateRequest("203.0.113.1:2000", ""), ep, deps) {
		t.Fatal("request beyond burst passed")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d want 429", w.Code)
	}
	secs, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || secs < 1 || secs > 60 {
		t.Fatalf("Retry-After = %q", w.Header().Get("Retry-After"))
	}
	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.2:1000", ""), ep, deps) {
		t.Fatal("a different client IP must have its own bucket")
	}
}

func TestRateLimitPerEndpointScope(t *testing.T) {
	ep := rateLimitedEndpoint("ep_1", &store.RateLimit{Requests: 1, PeriodSeconds: 60, Scope: store.RateLimitScopeEndpoint})
	deps := gateDeps{limiter: ratelimit.NewMemory()}
	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.1:1", ""), ep, deps) {
		t.Fatal("first request blocked")
	}
	if applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.2:1", ""), ep, deps) {
		t.Fatal("endpoint scope must be shared across client IPs")
	}
}

func TestRateLimitIsolatedPerEndpoint(t *testing.T) {
	rl := &store.RateLimit{Requests: 1, PeriodSeconds: 60}
	a := rateLimitedEndpoint("ep_a", rl)
	b := rateLimitedEndpoint("ep_b", rl)
	deps := gateDeps{limiter: ratelimit.NewMemory()}
	applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.1:1", ""), a, deps)
	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.1:1", ""), b, deps) {
		t.Fatal("limits must not leak across endpoints")
	}
}

func TestRateLimitKeyedByForwardedClientBehindTrustedProxy(t *testing.T) {
	ep := rateLimitedEndpoint("ep_1", &store.RateLimit{Requests: 1, PeriodSeconds: 60})
	deps := gateDeps{limiter: ratelimit.NewMemory(), trusted: mustTrusted(t, "10.0.0.0/8")}

	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("10.0.0.1:1", "198.51.100.1"), ep, deps) {
		t.Fatal("first client blocked")
	}
	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("10.0.0.1:1", "198.51.100.2"), ep, deps) {
		t.Fatal("second client behind same proxy must have its own bucket")
	}
	if applyPolicyGate(httptest.NewRecorder(), gateRequest("10.0.0.1:1", "198.51.100.1"), ep, deps) {
		t.Fatal("repeat client must be limited")
	}
}

func TestRateLimitIgnoresSpoofedForwardedFromUntrustedPeer(t *testing.T) {
	ep := rateLimitedEndpoint("ep_1", &store.RateLimit{Requests: 1, PeriodSeconds: 60})
	deps := gateDeps{limiter: ratelimit.NewMemory(), trusted: mustTrusted(t, "10.0.0.0/8")}
	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.1:1", "1.1.1.1"), ep, deps) {
		t.Fatal("first request blocked")
	}
	if applyPolicyGate(httptest.NewRecorder(), gateRequest("203.0.113.1:1", "2.2.2.2"), ep, deps) {
		t.Fatal("rotating X-Forwarded-For from an untrusted peer must not evade the limit")
	}
}

func TestRateLimitIPv6SharesPrefixBucket(t *testing.T) {
	ep := rateLimitedEndpoint("ep_1", &store.RateLimit{Requests: 1, PeriodSeconds: 60})
	deps := gateDeps{limiter: ratelimit.NewMemory()}
	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("[2001:db8:1:2::1]:1", ""), ep, deps) {
		t.Fatal("first request blocked")
	}
	if applyPolicyGate(httptest.NewRecorder(), gateRequest("[2001:db8:1:2::ffff]:1", ""), ep, deps) {
		t.Fatal("addresses in the same /64 must share a bucket")
	}
	if !applyPolicyGate(httptest.NewRecorder(), gateRequest("[2001:db8:1:3::1]:1", ""), ep, deps) {
		t.Fatal("a different /64 must have its own bucket")
	}
}

func TestIPPolicyHonorsTrustedProxies(t *testing.T) {
	trusted := mustTrusted(t, "10.0.0.0/8")
	cases := []struct {
		name   string
		policy store.EndpointPolicy
		remote string
		xff    string
		trust  bool
		want   bool
	}{
		{"allow: client from xff via trusted proxy", store.EndpointPolicy{IPAllow: []string{"198.51.100.0/24"}}, "10.0.0.1:1", "198.51.100.9", true, true},
		{"allow: proxy address alone is not enough", store.EndpointPolicy{IPAllow: []string{"198.51.100.0/24"}}, "10.0.0.1:1", "203.0.113.9", true, false},
		{"allow: spoofed xff from untrusted peer ignored", store.EndpointPolicy{IPAllow: []string{"198.51.100.0/24"}}, "203.0.113.9:1", "198.51.100.9", true, false},
		{"allow: xff ignored without trusted proxies", store.EndpointPolicy{IPAllow: []string{"198.51.100.0/24"}}, "10.0.0.1:1", "198.51.100.9", false, false},
		{"deny: client from xff via trusted proxy", store.EndpointPolicy{IPDeny: []string{"198.51.100.0/24"}}, "10.0.0.1:1", "198.51.100.9", true, false},
		{"deny: proxy range does not match forwarded client", store.EndpointPolicy{IPDeny: []string{"10.0.0.0/8"}}, "10.0.0.1:1", "198.51.100.9", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pol := c.policy
			ep := &store.Endpoint{ID: "ep_1", Policy: &pol}
			deps := gateDeps{}
			if c.trust {
				deps.trusted = trusted
			}
			got := applyPolicyGate(httptest.NewRecorder(), gateRequest(c.remote, c.xff), ep, deps)
			if got != c.want {
				t.Fatalf("passed = %v want %v", got, c.want)
			}
		})
	}
}
