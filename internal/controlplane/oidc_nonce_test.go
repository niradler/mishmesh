package controlplane

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeProvider struct {
	srv      *httptest.Server
	idToken  func(nonce string) string
	clientID string
}

func makeIDToken(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".sig"
}

func newFakeProvider(t *testing.T, clientID string) *fakeProvider {
	t.Helper()
	p := &fakeProvider{clientID: clientID}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": p.srv.URL + "/auth",
			"token_endpoint":         p.srv.URL + "/token",
			"userinfo_endpoint":      p.srv.URL + "/userinfo",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at", "id_token": p.idToken(p.lastNonce())})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "g-123", "email": "gina@example.com", "email_verified": true, "name": "Gina"})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

var lastNonceValue string

func (p *fakeProvider) lastNonce() string { return lastNonceValue }

func newGoogleAPI(t *testing.T, p *fakeProvider) *httptest.Server {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.ConfigureAuth(AuthOptions{
		Enabled: true, PasswordEnabled: true, SignupMode: "org",
		GoogleClientID: p.clientID, GoogleClientSecret: "secret",
		RedirectURL: "http://127.0.0.1/api/v1/auth/google/callback", Issuer: p.srv.URL,
	})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func noRedirectClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func startGoogle(t *testing.T, c *http.Client, srv *httptest.Server) (state string) {
	t.Helper()
	resp, err := c.Get(srv.URL + "/api/v1/auth/google/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start status %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	if q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization url must carry state and nonce: %s", loc)
	}
	lastNonceValue = q.Get("nonce")
	return q.Get("state")
}

func TestGoogleCallbackNonceAndCookieCleanup(t *testing.T) {
	const clientID = "client-1"
	tests := []struct {
		name    string
		idToken func(nonce string) string
		want    int
	}{
		{"matching nonce", func(n string) string {
			return makeIDToken(map[string]any{"sub": "g-123", "nonce": n, "aud": clientID})
		}, http.StatusFound},
		{"audience list", func(n string) string {
			return makeIDToken(map[string]any{"sub": "g-123", "nonce": n, "aud": []string{"x", clientID}})
		}, http.StatusFound},
		{"wrong nonce", func(string) string {
			return makeIDToken(map[string]any{"sub": "g-123", "nonce": "attacker", "aud": clientID})
		}, http.StatusUnauthorized},
		{"missing nonce claim", func(string) string {
			return makeIDToken(map[string]any{"sub": "g-123", "aud": clientID})
		}, http.StatusUnauthorized},
		{"wrong audience", func(n string) string {
			return makeIDToken(map[string]any{"sub": "g-123", "nonce": n, "aud": "other"})
		}, http.StatusUnauthorized},
		{"subject mismatch", func(n string) string {
			return makeIDToken(map[string]any{"sub": "someone-else", "nonce": n, "aud": clientID})
		}, http.StatusUnauthorized},
		{"no id token", func(string) string { return "" }, http.StatusUnauthorized},
		{"malformed id token", func(string) string { return "not-a-jwt" }, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProvider(t, clientID)
			p.idToken = tc.idToken
			srv := newGoogleAPI(t, p)
			c := noRedirectClient()
			state := startGoogle(t, c, srv)

			resp, err := c.Get(srv.URL + "/api/v1/auth/google/callback?code=abc&state=" + url.QueryEscape(state))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("callback status %d want %d", resp.StatusCode, tc.want)
			}
			cleared := map[string]bool{}
			for _, ck := range resp.Cookies() {
				if ck.MaxAge < 0 {
					cleared[ck.Name] = true
				}
			}
			if !cleared[oauthStateCookie] || !cleared[oauthNonceCookie] {
				t.Fatalf("state and nonce cookies must be cleared, got %v", cleared)
			}
		})
	}
}

func TestGoogleCallbackStateReplayRejected(t *testing.T) {
	p := newFakeProvider(t, "client-1")
	p.idToken = func(n string) string {
		return makeIDToken(map[string]any{"sub": "g-123", "nonce": n, "aud": "client-1"})
	}
	srv := newGoogleAPI(t, p)
	c := noRedirectClient()
	state := startGoogle(t, c, srv)
	callback := srv.URL + "/api/v1/auth/google/callback?code=abc&state=" + url.QueryEscape(state)

	resp, err := c.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("first callback status %d", resp.StatusCode)
	}
	resp, err = c.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed callback status %d want 400", resp.StatusCode)
	}
}
