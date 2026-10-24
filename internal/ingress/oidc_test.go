package ingress

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func testGate(t *testing.T) *oidcGate {
	t.Helper()
	return newOIDCGate(nil, []byte("test-signing-key-0123456789abcdef"), newIdentityService(t), false, true, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestStateRoundTrip(t *testing.T) {
	g := testGate(t)
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
	g := testGate(t)
	tok := g.signState(stateClaims{Ep: "ep_1", Exp: time.Now().Add(time.Minute).Unix()})
	_, sig, _ := strings.Cut(tok, ".")
	forgedBody := base64.RawURLEncoding.EncodeToString([]byte(`{"ep":"ep_evil","exp":9999999999}`))
	if _, err := g.verifyState(forgedBody + "." + sig); err == nil {
		t.Fatal("tampered state should be rejected")
	}
}

func TestStateExpired(t *testing.T) {
	g := testGate(t)
	tok := g.signState(stateClaims{Ep: "ep_1", Exp: time.Now().Add(-time.Minute).Unix()})
	if _, err := g.verifyState(tok); err == nil {
		t.Fatal("expired state should be rejected")
	}
}

func TestOIDCSessionIsServerSide(t *testing.T) {
	g := testGate(t)
	ctx := context.Background()
	ep := &store.Endpoint{ID: "ep_1", OrgID: "org_1", Kind: store.KindHTTP, Policy: &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: "https://idp.invalid", ClientID: "c"}}}
	issued, err := g.sessions.IssueGuest(ctx, "org_1", "ep_1", "u@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pass := func(value string, ep *store.Endpoint) bool {
		r := httptest.NewRequest("GET", "http://app.local/", nil)
		r.AddCookie(&http.Cookie{Name: oidcSessionCookie, Value: value})
		return g.authenticate(httptest.NewRecorder(), r, ep)
	}
	if !pass(issued.Token, ep) {
		t.Fatal("valid server-side session must pass")
	}
	other := *ep
	other.ID = "ep_other"
	if pass(issued.Token, &other) {
		t.Fatal("session bound to ep_1 must not pass for ep_other")
	}
	narrowed := *ep
	narrowed.Policy = &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: "https://idp.invalid", ClientID: "c", AllowedDomains: []string{"corp.com"}}}
	if pass(issued.Token, &narrowed) {
		t.Fatal("allowlist must be re-checked on every request")
	}
	if err := g.sessions.RevokeSession(ctx, issued.Session.ID); err != nil {
		t.Fatal(err)
	}
	if pass(issued.Token, ep) {
		t.Fatal("revoked session must not pass")
	}
}

func TestOIDCGateFailsClosedWithoutSessions(t *testing.T) {
	g := newOIDCGate(nil, []byte("k"), nil, false, true, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ep := &store.Endpoint{ID: "ep_1", Kind: store.KindHTTP, Policy: &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: "https://idp.invalid", ClientID: "c"}}}
	w := httptest.NewRecorder()
	if g.authenticate(w, httptest.NewRequest("GET", "http://app.local/", nil), ep) || w.Code != http.StatusServiceUnavailable {
		t.Fatalf("gate without sessions must fail closed, got %d", w.Code)
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

func TestProviderRejectsInsecureIssuer(t *testing.T) {
	g := newOIDCGate(nil, []byte("k"), nil, false, false, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := g.provider(context.Background(), "http://accounts.google.com", false); err == nil {
		t.Fatal("non-https issuer must be rejected when loopback is disallowed")
	}
}

func TestProviderBlocksMetadataIssuer(t *testing.T) {
	g := newOIDCGate(nil, []byte("k"), nil, false, false, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := g.provider(context.Background(), "https://169.254.169.254", false)
	if err == nil {
		t.Fatal("metadata-IP issuer must be blocked by the SSRF dialer")
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

type idTokenSpec struct {
	iss, aud, email, nonce, alg string
	verified                    bool
	exp                         time.Time
}

func mintIDToken(t *testing.T, key *rsa.PrivateKey, kid string, s idTokenSpec) string {
	t.Helper()
	alg := s.alg
	if alg == "" {
		alg = "RS256"
	}
	header := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, `{"alg":%q,"kid":%q,"typ":"JWT"}`, alg, kid))
	pj, _ := json.Marshal(map[string]any{"iss": s.iss, "aud": s.aud, "email": s.email, "email_verified": s.verified, "exp": s.exp.Unix(), "nonce": s.nonce})
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

func jwksSet(t *testing.T, kid string, pub *rsa.PublicKey) *jose.JSONWebKeySet {
	t.Helper()
	var set jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(jwksJSON(kid, pub)), &set); err != nil {
		t.Fatal(err)
	}
	return &set
}

func TestVerifyIDToken(t *testing.T) {
	key, kid := genKey(t)
	keys := jwksSet(t, kid, &key.PublicKey)
	iss, aud, nonce := "https://idp.example.com", "client123", "n-1"
	now := time.Now()
	good := idTokenSpec{iss: iss, aud: aud, email: "u@x.com", nonce: nonce, verified: true, exp: now.Add(time.Hour)}

	if claims, err := verifyIDToken(mintIDToken(t, key, kid, good), keys, iss, aud, nonce, now); err != nil || claims.Email != "u@x.com" || !claims.verifiedEmail() {
		t.Fatalf("valid token: claims=%+v err=%v", claims, err)
	}

	otherKey, _ := genKey(t)
	cases := []struct {
		name   string
		key    *rsa.PrivateKey
		kid    string
		mutate func(*idTokenSpec)
	}{
		{"expired", key, kid, func(s *idTokenSpec) { s.exp = now.Add(-time.Hour) }},
		{"wrong audience", key, kid, func(s *idTokenSpec) { s.aud = "other" }},
		{"wrong issuer", key, kid, func(s *idTokenSpec) { s.iss = "https://evil" }},
		{"missing nonce", key, kid, func(s *idTokenSpec) { s.nonce = "" }},
		{"wrong nonce", key, kid, func(s *idTokenSpec) { s.nonce = "replayed" }},
		{"unlisted algorithm", key, kid, func(s *idTokenSpec) { s.alg = "RS512" }},
		{"forged signature", otherKey, kid, func(*idTokenSpec) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := good
			tc.mutate(&spec)
			if _, err := verifyIDToken(mintIDToken(t, tc.key, tc.kid, spec), keys, iss, aud, nonce, now); err == nil {
				t.Fatal("token was accepted")
			}
		})
	}

	if _, err := verifyIDToken(mintIDToken(t, key, "rotated", good), keys, iss, aud, nonce, now); !errors.Is(err, errUnknownKeyID) {
		t.Fatalf("unknown kid = %v, want errUnknownKeyID", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://idp.example.com"}`)) + "."
	if _, err := verifyIDToken(unsigned, keys, iss, aud, nonce, now); err == nil {
		t.Fatal("alg none was accepted")
	}
	if _, err := verifyIDToken(strings.Repeat("a", maxIDTokenBytes+1), keys, iss, aud, nonce, now); err == nil {
		t.Fatal("oversized token was accepted")
	}
}

type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server

	mu              sync.Mutex
	key             *rsa.PrivateKey
	kid             string
	email           string
	nonce           string
	discoveryIssuer string
	padJWKS         int
	jwksFetches     int
}

func newFakeIdP(t *testing.T, email string) *fakeIdP {
	t.Helper()
	key, kid := genKey(t)
	f := &fakeIdP{t: t, key: key, kid: kid, email: email}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) issuer() string { return f.srv.URL }

func (f *fakeIdP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		iss := f.issuer()
		if f.discoveryIssuer != "" {
			iss = f.discoveryIssuer
		}
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`, iss, f.issuer()+"/authorize", f.issuer()+"/token", f.issuer()+"/jwks")
	case "/jwks":
		f.jwksFetches++
		doc := jwksJSON(f.kid, &f.key.PublicKey)
		if f.padJWKS > 0 {
			doc = doc[:len(doc)-1] + `,"pad":"` + strings.Repeat("x", f.padJWKS) + `"}`
		}
		io.WriteString(w, doc)
	case "/token":
		tok := mintIDToken(f.t, f.key, f.kid, idTokenSpec{iss: f.issuer(), aud: "client123", email: f.email, nonce: f.nonce, verified: true, exp: time.Now().Add(time.Hour)})
		fmt.Fprintf(w, `{"id_token":%q}`, tok)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeIdP) set(update func(*fakeIdP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f)
}

func oidcEndpointFixture(t *testing.T, issuer string, domains []string) (*oidcGate, *store.Endpoint) {
	t.Helper()
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
		Policy: &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: issuer, ClientID: "client123", ClientSecret: "secret", AllowedDomains: domains}},
	}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	return newOIDCGate(data, []byte("test-signing-key-0123456789abcdef"), newIdentityService(t), false, true, false, slog.New(slog.NewTextHandler(io.Discard, nil))), ep
}

func runOIDCLogin(t *testing.T, g *oidcGate, ep *store.Endpoint, idp *fakeIdP) *httptest.ResponseRecorder {
	t.Helper()
	w1 := httptest.NewRecorder()
	if g.authenticate(w1, httptest.NewRequest("GET", "http://app.local/secret", nil), ep) {
		t.Fatal("unauthenticated request must not pass")
	}
	if w1.Code != http.StatusFound {
		t.Fatalf("expected 302 to provider, got %d", w1.Code)
	}
	loc, _ := url.Parse(w1.Result().Header.Get("Location"))
	state, nonce := loc.Query().Get("state"), loc.Query().Get("nonce")
	if state == "" || nonce == "" {
		t.Fatal("authorization redirect lacks state or nonce")
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
	if nonce == stateCookie.Value {
		t.Fatal("the id token nonce exposes the state cookie value")
	}
	idp.set(func(f *fakeIdP) { f.nonce = nonce })
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "http://app.local"+oidcCallbackPath+"?code=abc&state="+url.QueryEscape(state), nil)
	r2.AddCookie(stateCookie)
	g.handleCallback(w2, r2)
	return w2
}

func TestOIDCFullCallbackFlow(t *testing.T) {
	idp := newFakeIdP(t, "dev@corp.com")
	g, ep := oidcEndpointFixture(t, idp.issuer(), []string{"corp.com"})
	w2 := runOIDCLogin(t, g, ep, idp)
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

func TestOIDCCallbackRejectsReplayedNonce(t *testing.T) {
	idp := newFakeIdP(t, "dev@corp.com")
	g, ep := oidcEndpointFixture(t, idp.issuer(), nil)
	w1 := httptest.NewRecorder()
	g.authenticate(w1, httptest.NewRequest("GET", "http://app.local/x", nil), ep)
	loc, _ := url.Parse(w1.Result().Header.Get("Location"))
	idp.set(func(f *fakeIdP) { f.nonce = "nonce-from-another-login" })
	r2 := httptest.NewRequest("GET", "http://app.local"+oidcCallbackPath+"?code=abc&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	r2.AddCookie(w1.Result().Cookies()[0])
	w2 := httptest.NewRecorder()
	g.handleCallback(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("id token with a foreign nonce = %d, want 401", w2.Code)
	}
}

func TestOIDCCallbackDeniesDisallowedDomain(t *testing.T) {
	idp := newFakeIdP(t, "intruder@evil.com")
	g, ep := oidcEndpointFixture(t, idp.issuer(), []string{"corp.com"})
	if w2 := runOIDCLogin(t, g, ep, idp); w2.Code != http.StatusForbidden {
		t.Fatalf("disallowed domain should be 403, got %d", w2.Code)
	}
}

func TestOIDCRefreshesKeysOnRotation(t *testing.T) {
	idp := newFakeIdP(t, "dev@corp.com")
	g, ep := oidcEndpointFixture(t, idp.issuer(), nil)
	if w := runOIDCLogin(t, g, ep, idp); w.Code != http.StatusFound {
		t.Fatalf("first login = %d", w.Code)
	}
	rotated, _ := genKey(t)
	idp.set(func(f *fakeIdP) { f.key, f.kid = rotated, "kid-2" })
	if w := runOIDCLogin(t, g, ep, idp); w.Code != http.StatusFound {
		t.Fatalf("login after key rotation = %d: %s", w.Code, w.Body)
	}
	idp.set(func(f *fakeIdP) { f.kid = "kid-3" })
	if w := runOIDCLogin(t, g, ep, idp); w.Code != http.StatusUnauthorized {
		t.Fatalf("second unknown kid within the refresh window = %d, want 401", w.Code)
	}
	idp.mu.Lock()
	defer idp.mu.Unlock()
	if idp.jwksFetches != 2 {
		t.Fatalf("jwks fetched %d times, want 2 (initial + one rotation refresh)", idp.jwksFetches)
	}
}

func TestOIDCProviderRejectsBadDiscovery(t *testing.T) {
	t.Run("issuer mismatch", func(t *testing.T) {
		idp := newFakeIdP(t, "dev@corp.com")
		idp.set(func(f *fakeIdP) { f.discoveryIssuer = "https://accounts.example.com" })
		g, _ := oidcEndpointFixture(t, idp.issuer(), nil)
		if _, err := g.provider(context.Background(), idp.issuer(), false); err == nil {
			t.Fatal("discovery for a different issuer was accepted")
		}
	})
	t.Run("oversized jwks", func(t *testing.T) {
		idp := newFakeIdP(t, "dev@corp.com")
		idp.set(func(f *fakeIdP) { f.padJWKS = maxOIDCDocumentSize })
		g, _ := oidcEndpointFixture(t, idp.issuer(), nil)
		if _, err := g.provider(context.Background(), idp.issuer(), false); err == nil {
			t.Fatal("oversized jwks was accepted")
		}
	})
}
