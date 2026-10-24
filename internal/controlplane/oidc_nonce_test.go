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
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeProvider struct {
	srv         *httptest.Server
	idToken     func(nonce string) string
	clientID    string
	tokenStatus int
	tokenBody   []byte
}

func makeIDToken(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".sig"
}

func (p *fakeProvider) claims(nonce string, overrides map[string]any) map[string]any {
	c := map[string]any{"iss": p.srv.URL, "exp": time.Now().Add(time.Hour).Unix(), "sub": "g-123", "nonce": nonce, "aud": p.clientID}
	for k, v := range overrides {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
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
		if p.tokenStatus != 0 {
			w.WriteHeader(p.tokenStatus)
		}
		if p.tokenBody != nil {
			_, _ = w.Write(p.tokenBody)
			return
		}
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
	expired := time.Now().Add(-time.Hour).Unix()
	tests := []struct {
		name        string
		claims      map[string]any
		rawToken    *string
		tokenStatus int
		tokenBody   []byte
		want        int
	}{
		{name: "matching nonce", want: http.StatusFound},
		{name: "audience list", claims: map[string]any{"aud": []string{"x", clientID}}, want: http.StatusFound},
		{name: "wrong nonce", claims: map[string]any{"nonce": "attacker"}, want: http.StatusUnauthorized},
		{name: "missing nonce claim", claims: map[string]any{"nonce": nil}, want: http.StatusUnauthorized},
		{name: "wrong audience", claims: map[string]any{"aud": "other"}, want: http.StatusUnauthorized},
		{name: "subject mismatch", claims: map[string]any{"sub": "someone-else"}, want: http.StatusUnauthorized},
		{name: "missing subject", claims: map[string]any{"sub": nil}, want: http.StatusUnauthorized},
		{name: "wrong issuer", claims: map[string]any{"iss": "https://evil.example.com"}, want: http.StatusUnauthorized},
		{name: "missing issuer", claims: map[string]any{"iss": nil}, want: http.StatusUnauthorized},
		{name: "expired", claims: map[string]any{"exp": expired}, want: http.StatusUnauthorized},
		{name: "missing expiry", claims: map[string]any{"exp": nil}, want: http.StatusUnauthorized},
		{name: "no id token", rawToken: ptr(""), want: http.StatusUnauthorized},
		{name: "malformed id token", rawToken: ptr("not-a-jwt"), want: http.StatusUnauthorized},
		{name: "token endpoint error", tokenStatus: http.StatusBadRequest, want: http.StatusUnauthorized},
		{name: "token endpoint oversized", tokenBody: []byte(`{"access_token":"` + strings.Repeat("a", maxOIDCResponseBytes) + `"}`), want: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProvider(t, clientID)
			p.tokenStatus = tc.tokenStatus
			p.tokenBody = tc.tokenBody
			p.idToken = func(n string) string {
				if tc.rawToken != nil {
					return *tc.rawToken
				}
				return makeIDToken(p.claims(n, tc.claims))
			}
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

func ptr[T any](v T) *T { return &v }

func TestIssuerAcceptedGoogleDefault(t *testing.T) {
	c := &authConfig{}
	for _, tc := range []struct {
		iss  string
		want bool
	}{
		{"https://accounts.google.com", true},
		{"accounts.google.com", true},
		{"https://accounts.google.com.evil.com", false},
		{"", false},
	} {
		if got := c.issuerAccepted(tc.iss); got != tc.want {
			t.Errorf("issuerAccepted(%q) = %v want %v", tc.iss, got, tc.want)
		}
	}
	custom := &authConfig{issuer: "https://idp.example.com/"}
	if !custom.issuerAccepted("https://idp.example.com") || custom.issuerAccepted("accounts.google.com") {
		t.Fatal("custom issuer must match exactly")
	}
}

func TestGoogleCallbackStateReplayRejected(t *testing.T) {
	p := newFakeProvider(t, "client-1")
	p.idToken = func(n string) string { return makeIDToken(p.claims(n, nil)) }
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
