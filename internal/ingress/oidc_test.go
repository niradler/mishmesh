package ingress

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func testGate() *oidcGate {
	return newOIDCGate(nil, []byte("test-signing-key-0123456789abcdef"), false, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestStateRoundTrip(t *testing.T) {
	g := testGate()
	tok := g.signState(stateClaims{Ep: "ep_1", Ret: "https://x/y", Nonce: "n1", Exp: time.Now().Add(time.Minute).Unix()})
	got, err := g.verifyState(tok)
	if err != nil {
		t.Fatalf("verifyState: %v", err)
	}
	if got.Ep != "ep_1" || got.Ret != "https://x/y" || got.Nonce != "n1" {
		t.Fatalf("claims mismatch: %+v", got)
	}
}

func TestStateTamperRejected(t *testing.T) {
	g := testGate()
	tok := g.signState(stateClaims{Ep: "ep_1", Exp: time.Now().Add(time.Minute).Unix()})
	_, sig, _ := strings.Cut(tok, ".")
	forgedBody := base64.RawURLEncoding.EncodeToString([]byte(`{"ep":"ep_evil","exp":9999999999}`))
	if _, err := g.verifyState(forgedBody + "." + sig); err == nil {
		t.Fatal("tampered state should be rejected")
	}
}

func TestStateExpired(t *testing.T) {
	g := testGate()
	tok := g.signState(stateClaims{Ep: "ep_1", Exp: time.Now().Add(-time.Minute).Unix()})
	if _, err := g.verifyState(tok); err == nil {
		t.Fatal("expired state should be rejected")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	g := testGate()
	tok := g.signSession("ep_1", "u@example.com")
	email, ok := g.verifySession(tok, "ep_1")
	if !ok || email != "u@example.com" {
		t.Fatalf("verifySession: ok=%v email=%q", ok, email)
	}
	if _, ok := g.verifySession(tok, "ep_other"); ok {
		t.Fatal("session bound to ep_1 must not validate for ep_other")
	}
}

func TestAllowlistPermits(t *testing.T) {
	cases := []struct {
		name  string
		cfg   *store.OIDCEndpointAuth
		email string
		want  bool
	}{
		{"empty allows any", &store.OIDCEndpointAuth{}, "anyone@any.com", true},
		{"email match", &store.OIDCEndpointAuth{AllowedEmails: []string{"a@x.com"}}, "A@X.com", true},
		{"email miss", &store.OIDCEndpointAuth{AllowedEmails: []string{"a@x.com"}}, "b@x.com", false},
		{"domain match", &store.OIDCEndpointAuth{AllowedDomains: []string{"x.com"}}, "anyone@x.com", true},
		{"domain prefix @ tolerated", &store.OIDCEndpointAuth{AllowedDomains: []string{"@x.com"}}, "anyone@x.com", true},
		{"domain miss", &store.OIDCEndpointAuth{AllowedDomains: []string{"x.com"}}, "anyone@y.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowlistPermits(tc.email, tc.cfg); got != tc.want {
				t.Fatalf("allowlistPermits(%q) = %v want %v", tc.email, got, tc.want)
			}
		})
	}
}

func genKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, "kid-1"
}

func mintIDToken(t *testing.T, key *rsa.PrivateKey, kid, iss, aud, email string, verified bool, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"alg":"RS256","kid":%q,"typ":"JWT"}`, kid))
	pj, _ := json.Marshal(map[string]any{"iss": iss, "aud": aud, "email": email, "email_verified": verified, "exp": exp.Unix()})
	payload := base64.RawURLEncoding.EncodeToString(pj)
	digest := sha256.Sum256([]byte(header + "." + payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func jwksJSON(kid string, pub *rsa.PublicKey) string {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	return fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":%q,"n":%q,"e":%q}]}`, kid, n, e)
}

func TestVerifyIDToken(t *testing.T) {
	key, kid := genKey(t)
	keys := map[string]*rsa.PublicKey{kid: &key.PublicKey}
	iss, aud := "https://idp.example.com", "client123"
	now := time.Now()

	valid := mintIDToken(t, key, kid, iss, aud, "u@x.com", true, now.Add(time.Hour))
	if claims, err := verifyIDToken(valid, keys, iss, aud, now); err != nil || claims.Email != "u@x.com" {
		t.Fatalf("valid token: claims=%+v err=%v", claims, err)
	}

	expired := mintIDToken(t, key, kid, iss, aud, "u@x.com", true, now.Add(-time.Hour))
	if _, err := verifyIDToken(expired, keys, iss, aud, now); err == nil {
		t.Error("expired token should fail")
	}

	wrongAud := mintIDToken(t, key, kid, iss, "other", "u@x.com", true, now.Add(time.Hour))
	if _, err := verifyIDToken(wrongAud, keys, iss, aud, now); err == nil {
		t.Error("wrong aud should fail")
	}

	wrongIss := mintIDToken(t, key, kid, "https://evil", aud, "u@x.com", true, now.Add(time.Hour))
	if _, err := verifyIDToken(wrongIss, keys, iss, aud, now); err == nil {
		t.Error("wrong issuer should fail")
	}

	otherKey, _ := genKey(t)
	forged := mintIDToken(t, otherKey, kid, iss, aud, "u@x.com", true, now.Add(time.Hour))
	if _, err := verifyIDToken(forged, keys, iss, aud, now); err == nil {
		t.Error("token signed by unknown key should fail")
	}
}

func TestOIDCFullCallbackFlow(t *testing.T) {
	key, kid := genKey(t)

	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
				issuer+"/authorize", issuer+"/token", issuer+"/jwks")
		case "/jwks":
			io.WriteString(w, jwksJSON(kid, &key.PublicKey))
		case "/token":
			tok := mintIDToken(t, key, kid, issuer, "client123", "dev@corp.com", true, time.Now().Add(time.Hour))
			fmt.Fprintf(w, `{"id_token":%q}`, tok)
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	data, err := sqlite.Open(filepath.Join(t.TempDir(), "oidc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	ep := &store.Endpoint{
		ID: "ep_1", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP, CreatedAt: time.Now(),
		Policy: &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: issuer, ClientID: "client123", ClientSecret: "secret", AllowedDomains: []string{"corp.com"}}},
	}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}

	g := newOIDCGate(data, []byte("test-signing-key-0123456789abcdef"), false, slog.New(slog.NewTextHandler(io.Discard, nil)))

	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest("GET", "http://app.local/secret", nil)
	if g.authenticate(w1, r1, ep) {
		t.Fatal("unauthenticated request must not pass")
	}
	if w1.Code != http.StatusFound {
		t.Fatalf("expected 302 to provider, got %d", w1.Code)
	}
	loc, _ := url.Parse(w1.Result().Header.Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state in redirect")
	}
	var stateCookie *http.Cookie
	for _, c := range w1.Result().Cookies() {
		if c.Name == oidcStateCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("no state cookie set")
	}

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "http://app.local"+oidcCallbackPath+"?code=abc&state="+url.QueryEscape(state), nil)
	r2.AddCookie(stateCookie)
	g.handleCallback(w2, r2)
	if w2.Code != http.StatusFound {
		t.Fatalf("callback should redirect on success, got %d: %s", w2.Code, w2.Body)
	}
	var sessionCookie *http.Cookie
	for _, c := range w2.Result().Cookies() {
		if c.Name == oidcSessionCookie {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie set after successful callback")
	}
	if w2.Result().Header.Get("Location") != "http://app.local/secret" {
		t.Fatalf("should redirect back to original path, got %q", w2.Result().Header.Get("Location"))
	}

	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("GET", "http://app.local/secret", nil)
	r3.AddCookie(sessionCookie)
	if !g.authenticate(w3, r3, ep) {
		t.Fatal("request with valid session cookie should pass")
	}
}

func TestOIDCCallbackDeniesDisallowedDomain(t *testing.T) {
	key, kid := genKey(t)
	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`, issuer+"/authorize", issuer+"/token", issuer+"/jwks")
		case "/jwks":
			io.WriteString(w, jwksJSON(kid, &key.PublicKey))
		case "/token":
			tok := mintIDToken(t, key, kid, issuer, "client123", "intruder@evil.com", true, time.Now().Add(time.Hour))
			fmt.Fprintf(w, `{"id_token":%q}`, tok)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	data, _ := sqlite.Open(filepath.Join(t.TempDir(), "oidc2.db"))
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	ep := &store.Endpoint{
		ID: "ep_1", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP, CreatedAt: time.Now(),
		Policy: &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: issuer, ClientID: "client123", AllowedDomains: []string{"corp.com"}}},
	}
	_ = data.CreateEndpoint(ctx, ep)

	g := newOIDCGate(data, []byte("test-signing-key-0123456789abcdef"), false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w1 := httptest.NewRecorder()
	g.authenticate(w1, httptest.NewRequest("GET", "http://app.local/x", nil), ep)
	loc, _ := url.Parse(w1.Result().Header.Get("Location"))
	state := loc.Query().Get("state")
	var sc *http.Cookie
	for _, c := range w1.Result().Cookies() {
		if c.Name == oidcStateCookie {
			sc = c
		}
	}

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "http://app.local"+oidcCallbackPath+"?code=abc&state="+url.QueryEscape(state), nil)
	r2.AddCookie(sc)
	g.handleCallback(w2, r2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("disallowed domain should be 403, got %d", w2.Code)
	}
}
