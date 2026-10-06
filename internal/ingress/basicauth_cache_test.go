package ingress

import (
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestCheckBasicAuthCache(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cache := newTTLCache[struct{}](basicAuthCacheTTL)
	check := func(epID, user, pass, hashStr string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetBasicAuth(user, pass)
		return checkBasicAuth(r, epID, "alice", hashStr, cache)
	}

	tests := []struct {
		name string
		ep   string
		user string
		pass string
		hash string
		want bool
	}{
		{"correct password", "ep_1", "alice", "s3cret", string(hash), true},
		{"wrong password", "ep_1", "alice", "nope", string(hash), false},
		{"wrong user", "ep_1", "bob", "s3cret", string(hash), false},
		{"empty hash", "ep_1", "alice", "s3cret", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := check(tt.ep, tt.user, tt.pass, tt.hash); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("success is cached per endpoint and hash", func(t *testing.T) {
		if !check("ep_1", "alice", "s3cret", string(hash)) {
			t.Fatal("warm-up must pass")
		}
		cache.mu.RLock()
		n := len(cache.entries)
		cache.mu.RUnlock()
		if n != 1 {
			t.Fatalf("cache entries = %d, want 1", n)
		}
		if check("ep_2", "alice", "s3cret", "not-a-bcrypt-hash") {
			t.Fatal("cache entry must not leak to another endpoint or hash")
		}
		if check("ep_1", "alice", "s3cret", "not-a-bcrypt-hash") {
			t.Fatal("rotated hash must invalidate the cached success")
		}
	})

	t.Run("failure is never cached", func(t *testing.T) {
		if check("ep_3", "alice", "nope", string(hash)) {
			t.Fatal("wrong password must fail")
		}
		if check("ep_3", "alice", "nope", string(hash)) {
			t.Fatal("wrong password must keep failing")
		}
	})

	t.Run("nil cache still verifies", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetBasicAuth("alice", "s3cret")
		if !checkBasicAuth(r, "ep_1", "alice", string(hash), nil) {
			t.Fatal("nil cache must fall back to bcrypt")
		}
	})
}
