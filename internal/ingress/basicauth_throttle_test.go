package ingress

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestBasicAuthThrottlesRepeatedFailuresPerIP(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: "ep_1", Policy: &store.EndpointPolicy{BasicAuthUser: "alice", BasicAuthHash: string(hash)}}
	deps := gateDeps{limiter: ratelimit.NewMemory(), basic: newTTLCache[struct{}](basicAuthCacheTTL), basicLockout: newTTLCache[time.Time](basicAuthVerifyPeriod)}
	attempt := func(remote, pass string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
		r.RemoteAddr = remote + ":1234"
		r.SetBasicAuth("alice", pass)
		w := httptest.NewRecorder()
		applyPolicyGate(w, r, ep, deps)
		return w
	}

	for i := 0; i < basicAuthVerifyRequests; i++ {
		if w := attempt("203.0.113.9", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i, w.Code)
		}
	}
	w := attempt("203.0.113.9", "wrong")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d retry-after = %q, want 429 with Retry-After", w.Code, w.Header().Get("Retry-After"))
	}
	if w := attempt("203.0.113.9", "s3cret"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked-out client verified anyway: status = %d, want 429", w.Code)
	}
	if w := attempt("203.0.113.10", "s3cret"); w.Code != http.StatusOK {
		t.Fatalf("other client blocked: status = %d", w.Code)
	}
}

func TestBasicAuthUncachedSuccessesAreNotThrottled(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: "ep_1", Policy: &store.EndpointPolicy{BasicAuthUser: "alice", BasicAuthHash: string(hash)}}
	deps := gateDeps{limiter: ratelimit.NewMemory()}
	for i := 0; i < basicAuthVerifyRequests*3; i++ {
		r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.SetBasicAuth("alice", "s3cret")
		w := httptest.NewRecorder()
		if !applyPolicyGate(w, r, ep, deps) {
			t.Fatalf("successful login %d rejected with %d", i, w.Code)
		}
	}
}

func TestBasicAuthCachedSuccessBypassesThrottle(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: "ep_1", Policy: &store.EndpointPolicy{BasicAuthUser: "alice", BasicAuthHash: string(hash)}}
	deps := gateDeps{limiter: ratelimit.NewMemory(), basic: newTTLCache[struct{}](basicAuthCacheTTL), basicLockout: newTTLCache[time.Time](basicAuthVerifyPeriod)}
	for i := 0; i < basicAuthVerifyRequests*3; i++ {
		r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.SetBasicAuth("alice", "s3cret")
		w := httptest.NewRecorder()
		if !applyPolicyGate(w, r, ep, deps) {
			t.Fatalf("request %d rejected with %d", i, w.Code)
		}
	}
}
