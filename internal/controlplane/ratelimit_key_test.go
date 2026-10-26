package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
)

type keyRecorder struct{ keys []string }

func (k *keyRecorder) Allow(_ context.Context, key string, _ ratelimit.Limit) ratelimit.Decision {
	k.keys = append(k.keys, key)
	return ratelimit.Decision{Allowed: true}
}

func TestAuthRateKeyIgnoresSourcePort(t *testing.T) {
	for _, remote := range [][2]string{
		{"[fe80::1%eth0]:1111", "[fe80::1%eth0]:2222"},
		{"10.0.0.7:1111", "10.0.0.7:2222"},
	} {
		rec := &keyRecorder{}
		api := New(nil, nil, "", nil)
		api.SetLimiter(rec)
		for _, addr := range remote {
			r := httptest.NewRequest(http.MethodPost, "/login", nil)
			r.RemoteAddr = addr
			api.authRateAllowed(r, "")
		}
		if len(rec.keys) != 2 || rec.keys[0] != rec.keys[1] {
			t.Fatalf("remote %v: limiter keys differ per source port: %v", remote, rec.keys)
		}
	}
}
