package controlplane

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newLimitedAPI(t *testing.T, limiter ratelimit.Limiter, trusted []*net.IPNet) *httptest.Server {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "rl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true})
	api.SetLimiter(limiter)
	api.SetTrustedProxies(trusted)
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func loginStatus(t *testing.T, srv *httptest.Server, email, xff string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login", strings.NewReader(`{"email":"`+email+`","password":"wrongwrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestLoginRateLimitSharedAcrossNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	newNodeLimiter := func() ratelimit.Limiter {
		rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		return ratelimit.NewRedis(rdb, nil)
	}
	nodeA := newLimitedAPI(t, newNodeLimiter(), nil)
	nodeB := newLimitedAPI(t, newNodeLimiter(), nil)

	for i := 0; i < emailLimit.Capacity(); i++ {
		if got := loginStatus(t, nodeA, "victim@example.com", ""); got == http.StatusTooManyRequests {
			t.Fatalf("attempt %d on node A limited too early", i)
		}
	}
	if got := loginStatus(t, nodeB, "victim@example.com", ""); got != http.StatusTooManyRequests {
		t.Fatalf("node B status = %d want 429 (attempts on node A must count)", got)
	}
}

func TestLoginRateLimitUsesForwardedClientBehindTrustedProxy(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := newLimitedAPI(t, ratelimit.NewMemory(), []*net.IPNet{loopback})

	for i := 0; i < ipLimit.Capacity(); i++ {
		loginStatus(t, srv, "", "198.51.100.1")
	}
	if got := loginStatus(t, srv, "", "198.51.100.1"); got != http.StatusTooManyRequests {
		t.Fatalf("exhausted client status = %d want 429", got)
	}
	if got := loginStatus(t, srv, "", "198.51.100.2"); got == http.StatusTooManyRequests {
		t.Fatal("a different forwarded client must not share the bucket")
	}
}
