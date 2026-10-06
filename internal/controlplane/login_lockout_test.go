package controlplane

import (
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
)

func TestLoginLockoutIsPerClientAndEmailPair(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := newLimitedAPI(t, ratelimit.NewMemory(), []*net.IPNet{loopback})

	for i := 0; i < pairLimit.Capacity(); i++ {
		if got := loginStatus(t, srv, "victim@example.com", "198.51.100.1"); got == http.StatusTooManyRequests {
			t.Fatalf("attempt %d limited too early", i)
		}
	}
	if got := loginStatus(t, srv, "victim@example.com", "198.51.100.1"); got != http.StatusTooManyRequests {
		t.Fatalf("attacker pair status = %d want 429", got)
	}
	if got := loginStatus(t, srv, "victim@example.com", "198.51.100.2"); got == http.StatusTooManyRequests {
		t.Fatal("a different client must still be able to attempt the same email")
	}
	if got := loginStatus(t, srv, "other@example.com", "198.51.100.1"); got == http.StatusTooManyRequests {
		t.Fatal("the same client must still be able to attempt a different email")
	}
}

func TestPasswordMatchesBurnsBcryptForMissingUser(t *testing.T) {
	passwordMatches("", "warmup")
	start := time.Now()
	for i := 0; i < 3; i++ {
		if passwordMatches("", "whatever") {
			t.Fatal("empty hash must never match")
		}
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("missing-user path took %v, expected a bcrypt-sized delay", elapsed)
	}
	real, err := bcrypt.GenerateFromPassword([]byte("right"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if !passwordMatches(string(real), "right") || passwordMatches(string(real), "wrong") {
		t.Fatal("real hash comparison broken")
	}
}
