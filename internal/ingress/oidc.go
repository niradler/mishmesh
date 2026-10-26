package ingress

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/safehttp"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	oidcCallbackPath  = "/_mishmesh/oidc/callback"
	oidcSessionCookie = "mm_oidc"
	oidcStateCookie   = "mm_oidc_state"
	oidcStateTTL      = 10 * time.Minute
	oidcSessionTTL    = 12 * time.Hour
	jwksTTL           = time.Hour
	jwksRefreshMin    = time.Minute

	tokenPurposeState = "state"
)

type oidcGate struct {
	data          store.DataStore
	signKey       []byte
	sessions      *identity.Service
	cookieSecure  bool
	allowLoopback bool
	trusted       []*net.IPNet
	log           *slog.Logger
	httpClient    *http.Client
	mu            sync.Mutex
	providers     map[string]*oidcIssuer
}

type oidcIssuer struct {
	mu          sync.Mutex
	prov        *oidcProvider
	lastRefresh time.Time
}

type oidcProvider struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	keys                  *jose.JSONWebKeySet
	keysAt                time.Time
}

func newOIDCGate(data store.DataStore, signKey []byte, sessions *identity.Service, cookieSecure, allowLoopback, allowPrivate bool, trusted []*net.IPNet, log *slog.Logger) *oidcGate {
	if log == nil {
		log = slog.Default()
	}
	return &oidcGate{
		data:          data,
		signKey:       signKey,
		sessions:      sessions,
		cookieSecure:  cookieSecure,
		allowLoopback: allowLoopback,
		trusted:       trusted,
		log:           log,
		httpClient:    safehttp.Client(allowLoopback, allowPrivate),
		providers:     make(map[string]*oidcIssuer),
	}
}

func (g *oidcGate) authenticate(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) bool {
	if g.sessions == nil {
		http.Error(w, "endpoint oidc sessions unavailable", http.StatusServiceUnavailable)
		return false
	}
	if c, err := r.Cookie(oidcSessionCookie); err == nil && c.Value != "" {
		email, err := g.sessions.AuthenticateGuest(r.Context(), c.Value, ep.ID)
		if errors.Is(err, identity.ErrUnavailable) {
			g.log.Error("oidc session lookup failed", "endpoint_id", ep.ID, "err", err)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "endpoint oidc sessions unavailable", http.StatusServiceUnavailable)
			return false
		}
		if err == nil && allowlistPermits(email, ep.Policy.OIDC) {
			return true
		}
	}
	g.startAuth(w, r, ep)
	return false
}

func (g *oidcGate) startAuth(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) {
	prov, err := g.provider(r.Context(), ep.Policy.OIDC.Issuer, false)
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
	returnURL := requestScheme(r, g.trusted) + "://" + r.Host + r.URL.RequestURI()
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
	q.Set("nonce", idTokenNonce(nonce))
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
	prov, err := g.provider(r.Context(), cfg.Issuer, false)
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
	claims, err := verifyIDToken(rawID, prov.keys, prov.Issuer, cfg.ClientID, idTokenNonce(state.Nonce), time.Now())
	if errors.Is(err, errUnknownKeyID) {
		if prov, err = g.provider(r.Context(), cfg.Issuer, true); err == nil {
			claims, err = verifyIDToken(rawID, prov.keys, prov.Issuer, cfg.ClientID, idTokenNonce(state.Nonce), time.Now())
		}
	}
	if err != nil {
		g.log.Warn("oidc id token invalid", "err", err)
		http.Error(w, "oidc token invalid", http.StatusUnauthorized)
		return
	}
	if !claims.verifiedEmail() || !allowlistPermits(claims.Email, cfg) {
		http.Error(w, "access denied for this account", http.StatusForbidden)
		return
	}
	if g.sessions == nil {
		http.Error(w, "endpoint oidc sessions unavailable", http.StatusServiceUnavailable)
		return
	}
	issued, err := g.sessions.IssueGuest(r.Context(), ep.OrgID, ep.ID, strings.ToLower(claims.Email), oidcSessionTTL)
	if err != nil {
		g.log.Error("oidc session issue failed", "endpoint_id", ep.ID, "err", err)
		http.Error(w, "endpoint oidc sessions unavailable", http.StatusServiceUnavailable)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: oidcSessionCookie, Value: issued.Token, Path: "/", HttpOnly: true,
		Secure: g.cookieSecure, SameSite: http.SameSiteLaxMode, Expires: issued.Session.ExpiresAt,
	})
	http.Redirect(w, r, state.Ret, http.StatusFound)
}

func (g *oidcGate) redirectURI(r *http.Request) string {
	return requestScheme(r, g.trusted) + "://" + r.Host + oidcCallbackPath
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
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := decodeLimitedJSON(resp, &tok); err != nil {
		return "", err
	}
	if tok.IDToken == "" {
		return "", errors.New("no id_token in token response")
	}
	return tok.IDToken, nil
}

func (g *oidcGate) provider(ctx context.Context, issuer string, unknownKey bool) (*oidcProvider, error) {
	issuer = strings.TrimRight(issuer, "/")
	if issuer == "" {
		return nil, errors.New("empty issuer")
	}
	if !g.allowLoopback && !strings.HasPrefix(strings.ToLower(issuer), "https://") {
		return nil, errors.New("oidc issuer must use https")
	}
	g.mu.Lock()
	entry, ok := g.providers[issuer]
	if !ok {
		entry = &oidcIssuer{}
		g.providers[issuer] = entry
	}
	g.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	now := time.Now()
	if p := entry.prov; p != nil && now.Sub(p.keysAt) < jwksTTL && (!unknownKey || now.Sub(entry.lastRefresh) < jwksRefreshMin) {
		return p, nil
	}
	if unknownKey {
		entry.lastRefresh = now
	}
	disc, err := fetchJSON[oidcProvider](ctx, g.httpClient, issuer+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" || disc.JWKSURI == "" {
		return nil, errors.New("incomplete discovery document")
	}
	if strings.TrimRight(disc.Issuer, "/") != issuer {
		return nil, fmt.Errorf("discovery issuer %q does not match the configured issuer", disc.Issuer)
	}
	keys, err := fetchJWKS(ctx, g.httpClient, disc.JWKSURI)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	disc.keys = keys
	disc.keysAt = now
	entry.prov = disc
	return disc, nil
}

func idTokenNonce(stateNonce string) string {
	sum := sha256.Sum256([]byte("mishmesh-oidc-nonce:" + stateNonce))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type stateClaims struct {
	Ep    string `json:"ep"`
	Ret   string `json:"ret"`
	Nonce string `json:"nonce"`
	Exp   int64  `json:"exp"`
}

func (g *oidcGate) signState(c stateClaims) string {
	payload, _ := json.Marshal(c)
	return g.sign(tokenPurposeState, payload)
}

func (g *oidcGate) verifyState(token string) (*stateClaims, error) {
	payload, err := g.unsign(tokenPurposeState, token)
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

func (g *oidcGate) purposeMAC(purpose string) hash.Hash {
	derive := hmac.New(sha256.New, g.signKey)
	derive.Write([]byte("mishmesh-oidc-token-v1:" + purpose))
	return hmac.New(sha256.New, derive.Sum(nil))
}

func (g *oidcGate) sign(purpose string, payload []byte) string {
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := g.purposeMAC(purpose)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (g *oidcGate) unsign(purpose, token string) ([]byte, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, errors.New("malformed token")
	}
	mac := g.purposeMAC(purpose)
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

func requestScheme(r *http.Request, trusted []*net.IPNet) string {
	if requestIsHTTPS(r, trusted) {
		return "https"
	}
	return "http"
}
