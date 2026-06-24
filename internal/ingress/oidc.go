package ingress

import (
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	oidcCallbackPath  = "/_mishmesh/oidc/callback"
	oidcSessionCookie = "mm_oidc"
	oidcStateCookie   = "mm_oidc_state"
	oidcStateTTL      = 10 * time.Minute
	oidcSessionTTL    = 12 * time.Hour
	jwksTTL           = time.Hour
)

type oidcGate struct {
	data         store.DataStore
	signKey      []byte
	cookieSecure bool
	log          *slog.Logger
	httpClient   *http.Client
	mu           sync.Mutex
	providers    map[string]*oidcProvider
}

type oidcProvider struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	keys                  map[string]*rsa.PublicKey
	keysAt                time.Time
}

func newOIDCGate(data store.DataStore, signKey []byte, cookieSecure bool, log *slog.Logger) *oidcGate {
	if log == nil {
		log = slog.Default()
	}
	return &oidcGate{
		data:         data,
		signKey:      signKey,
		cookieSecure: cookieSecure,
		log:          log,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		providers:    make(map[string]*oidcProvider),
	}
}

func (g *oidcGate) authenticate(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) bool {
	if c, err := r.Cookie(oidcSessionCookie); err == nil {
		if email, ok := g.verifySession(c.Value, ep.ID); ok && allowlistPermits(email, ep.Policy.OIDC) {
			return true
		}
	}
	g.startAuth(w, r, ep)
	return false
}

func (g *oidcGate) startAuth(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) {
	prov, err := g.provider(r.Context(), ep.Policy.OIDC.Issuer)
	if err != nil {
		g.log.Warn("oidc discovery failed", "issuer", ep.Policy.OIDC.Issuer, "err", err)
		http.Error(w, "oidc provider unavailable", http.StatusBadGateway)
		return
	}
	nonce, err := randomString()
	if err != nil {
		http.Error(w, "oidc state failed", http.StatusInternalServerError)
		return
	}
	returnURL := requestScheme(r) + "://" + r.Host + r.URL.RequestURI()
	state := g.signState(stateClaims{Ep: ep.ID, Ret: returnURL, Nonce: nonce, Exp: time.Now().Add(oidcStateTTL).Unix()})

	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: nonce, Path: "/", HttpOnly: true,
		Secure: g.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: int(oidcStateTTL.Seconds()),
	})

	q := url.Values{}
	q.Set("client_id", ep.Policy.OIDC.ClientID)
	q.Set("redirect_uri", g.redirectURI(r))
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	http.Redirect(w, r, prov.AuthorizationEndpoint+"?"+q.Encode(), http.StatusFound)
}

func (g *oidcGate) handleCallback(w http.ResponseWriter, r *http.Request) {
	state, err := g.verifyState(r.URL.Query().Get("state"))
	if err != nil {
		http.Error(w, "invalid oidc state", http.StatusBadRequest)
		return
	}
	c, err := r.Cookie(oidcStateCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state.Nonce)) != 1 {
		http.Error(w, "oidc state mismatch", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/", MaxAge: -1})

	ep, err := g.data.GetEndpoint(r.Context(), state.Ep)
	if err != nil || ep.Policy == nil || ep.Policy.OIDC == nil {
		http.Error(w, "unknown endpoint", http.StatusBadRequest)
		return
	}
	cfg := ep.Policy.OIDC
	prov, err := g.provider(r.Context(), cfg.Issuer)
	if err != nil {
		http.Error(w, "oidc provider unavailable", http.StatusBadGateway)
		return
	}
	rawID, err := g.exchangeCode(r.Context(), prov, cfg, r.URL.Query().Get("code"), g.redirectURI(r))
	if err != nil {
		g.log.Warn("oidc code exchange failed", "err", err)
		http.Error(w, "oidc exchange failed", http.StatusUnauthorized)
		return
	}
	claims, err := verifyIDToken(rawID, prov.keys, cfg.Issuer, cfg.ClientID, time.Now())
	if err != nil {
		g.log.Warn("oidc id token invalid", "err", err)
		http.Error(w, "oidc token invalid", http.StatusUnauthorized)
		return
	}
	if !claims.verifiedEmail() || !allowlistPermits(claims.Email, cfg) {
		http.Error(w, "access denied for this account", http.StatusForbidden)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: oidcSessionCookie, Value: g.signSession(ep.ID, claims.Email), Path: "/", HttpOnly: true,
		Secure: g.cookieSecure, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(oidcSessionTTL),
	})
	http.Redirect(w, r, state.Ret, http.StatusFound)
}

func (g *oidcGate) redirectURI(r *http.Request) string {
	return requestScheme(r) + "://" + r.Host + oidcCallbackPath
}

func (g *oidcGate) exchangeCode(ctx context.Context, prov *oidcProvider, cfg *store.OIDCEndpointAuth, code, redirectURI string) (string, error) {
	if code == "" {
		return "", errors.New("missing code")
	}
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.IDToken == "" {
		return "", errors.New("no id_token in token response")
	}
	return tok.IDToken, nil
}

func (g *oidcGate) provider(ctx context.Context, issuer string) (*oidcProvider, error) {
	issuer = strings.TrimRight(issuer, "/")
	if issuer == "" {
		return nil, errors.New("empty issuer")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if p, ok := g.providers[issuer]; ok && time.Since(p.keysAt) < jwksTTL {
		return p, nil
	}
	disc, err := fetchJSON[oidcProvider](ctx, g.httpClient, issuer+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" || disc.JWKSURI == "" {
		return nil, errors.New("incomplete discovery document")
	}
	keys, err := fetchJWKS(ctx, g.httpClient, disc.JWKSURI)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	disc.keys = keys
	disc.keysAt = time.Now()
	g.providers[issuer] = disc
	return disc, nil
}

type stateClaims struct {
	Ep    string `json:"ep"`
	Ret   string `json:"ret"`
	Nonce string `json:"nonce"`
	Exp   int64  `json:"exp"`
}

func (g *oidcGate) signState(c stateClaims) string {
	payload, _ := json.Marshal(c)
	return g.sign(payload)
}

func (g *oidcGate) verifyState(token string) (*stateClaims, error) {
	payload, err := g.unsign(token)
	if err != nil {
		return nil, err
	}
	var c stateClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, err
	}
	if time.Now().Unix() > c.Exp {
		return nil, errors.New("state expired")
	}
	return &c, nil
}

type sessionClaims struct {
	Ep    string `json:"ep"`
	Email string `json:"email"`
	Exp   int64  `json:"exp"`
}

func (g *oidcGate) signSession(epID, email string) string {
	payload, _ := json.Marshal(sessionClaims{Ep: epID, Email: email, Exp: time.Now().Add(oidcSessionTTL).Unix()})
	return g.sign(payload)
}

func (g *oidcGate) verifySession(token, epID string) (string, bool) {
	payload, err := g.unsign(token)
	if err != nil {
		return "", false
	}
	var c sessionClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", false
	}
	if c.Ep != epID || time.Now().Unix() > c.Exp {
		return "", false
	}
	return c.Email, true
}

func (g *oidcGate) sign(payload []byte) string {
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, g.signKey)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (g *oidcGate) unsign(token string) ([]byte, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, g.signKey)
	mac.Write([]byte(body))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return nil, errors.New("bad signature")
	}
	return base64.RawURLEncoding.DecodeString(body)
}

func allowlistPermits(email string, cfg *store.OIDCEndpointAuth) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.AllowedEmails) == 0 && len(cfg.AllowedDomains) == 0 {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for _, e := range cfg.AllowedEmails {
		if strings.EqualFold(strings.TrimSpace(e), email) {
			return true
		}
	}
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return false
	}
	for _, d := range cfg.AllowedDomains {
		if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(d, "@")), domain) {
			return true
		}
	}
	return false
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return "https"
	}
	return "http"
}
