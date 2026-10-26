package ingress

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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

func TestBasicAuthConcurrentFailuresCannotOutrunLockout(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var verifications atomic.Int32
	original := compareBasicAuthPassword
	compareBasicAuthPassword = func(hash, pass []byte) error {
		verifications.Add(1)
		time.Sleep(5 * time.Millisecond)
		return original(hash, pass)
	}
	t.Cleanup(func() { compareBasicAuthPassword = original })

	ep := &store.Endpoint{ID: "ep_1", Policy: &store.EndpointPolicy{BasicAuthUser: "alice", BasicAuthHash: string(hash)}}
	deps := gateDeps{limiter: ratelimit.NewMemory(), basic: newTTLCache[struct{}](basicAuthCacheTTL), basicLockout: newTTLCache[time.Time](basicAuthVerifyPeriod), basicVerifying: &keyedLocks{}}
	const burst = 50
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
			r.RemoteAddr = "203.0.113.9:1234"
			r.SetBasicAuth("alice", "wrong")
			applyPolicyGate(httptest.NewRecorder(), r, ep, deps)
		}()
	}
	wg.Wait()
	if got := verifications.Load(); got > basicAuthVerifyRequests+1 {
		t.Fatalf("bcrypt ran %d times for a %d-request burst, want at most %d", got, burst, basicAuthVerifyRequests+1)
	}
}

func TestBasicAuthMissingCredentialsDoNotLockOut(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: "ep_1", Policy: &store.EndpointPolicy{BasicAuthUser: "alice", BasicAuthHash: string(hash)}}
	deps := gateDeps{limiter: ratelimit.NewMemory(), basic: newTTLCache[struct{}](basicAuthCacheTTL), basicLockout: newTTLCache[time.Time](basicAuthVerifyPeriod), basicVerifying: &keyedLocks{}}
	for i := 0; i < basicAuthVerifyRequests*3; i++ {
		r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		w := httptest.NewRecorder()
		applyPolicyGate(w, r, ep, deps)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("credential-less request %d: status = %d, want 401 with a login challenge", i, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.SetBasicAuth("alice", "s3cret")
	if w := httptest.NewRecorder(); !applyPolicyGate(w, r, ep, deps) {
		t.Fatalf("correct password after anonymous visits rejected with %d", w.Code)
	}
}
