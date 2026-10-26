package controlplane

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	googleIssuer         = "https://accounts.google.com"
	googleIssuerBare     = "accounts.google.com"
	oidcRequestTimeout   = 10 * time.Second
	maxOIDCResponseBytes = 1 << 20
	idTokenClockSkew     = 2 * time.Minute
)

var oidcHTTPClient = &http.Client{Timeout: oidcRequestTimeout}

const (
	oauthStateCookie = "mm_oauth_state"
	oauthNextCookie  = "mm_oauth_next"
)

func (a *API) googleStartHandler(w http.ResponseWriter, r *http.Request) {
	if !a.googleEnabled() {
		writeError(w, http.StatusForbidden, "google login not configured")
		return
	}
	disc, err := a.auth.discover(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "oidc discovery failed")
		return
	}
	state, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state failed")
		return
	}
	nonce, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "nonce failed")
		return
	}
	a.setShortLivedCookie(w, oauthStateCookie, state)
	a.setShortLivedCookie(w, oauthNonceCookie, nonce)
	if next := r.URL.Query().Get("next"); localNext(next) {
		a.setShortLivedCookie(w, oauthNextCookie, next)
	}
	q := url.Values{}
	q.Set("nonce", nonce)
	q.Set("client_id", a.auth.googleClientID)
	q.Set("redirect_uri", a.auth.redirectURL)
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	http.Redirect(w, r, disc.AuthorizationEndpoint+"?"+q.Encode(), http.StatusFound)
}

func (a *API) googleCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !a.googleEnabled() {
		writeError(w, http.StatusForbidden, "google login not configured")
		return
	}
	state := r.URL.Query().Get("state")
	c, err := r.Cookie(oauthStateCookie)
	nonceCookie, nonceErr := r.Cookie(oauthNonceCookie)
	a.clearOAuthCookies(w)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		writeError(w, http.StatusBadRequest, "invalid oauth state")
		return
	}
	if nonceErr != nil || nonceCookie.Value == "" {
		writeError(w, http.StatusBadRequest, "invalid oauth state")
		return
	}
	disc, err := a.auth.discover(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "oidc discovery failed")
		return
	}
	profile, err := a.auth.exchangeAndProfile(r.Context(), disc, r.URL.Query().Get("code"), nonceCookie.Value, a.now())
	if err != nil {
		a.log.Warn("google oauth exchange failed", "err", err)
		writeError(w, http.StatusUnauthorized, "oauth exchange failed")
		return
	}
	user, err := a.upsertOIDCUser(r.Context(), profile)
	if errors.Is(err, errEmailUnverified) {
		writeError(w, http.StatusForbidden, "email not verified by provider; sign in with password and link from settings")
		return
	}
	if errors.Is(err, errSignupClosed) {
		writeError(w, http.StatusForbidden, "registration is by invitation only")
		return
	}
	if errors.Is(err, errPasswordAccountExists) {
		writeError(w, http.StatusConflict, "an account with this email already exists; sign in with your password")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user upsert failed")
		return
	}
	orgID, ok := a.primaryOrg(r.Context(), user.ID)
	if !ok {
		writeError(w, http.StatusForbidden, "no organization membership")
		return
	}
	if err := a.issueSession(r.Context(), w, user.ID, orgID, authz.AuthMethodOIDC, time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	http.Redirect(w, r, a.takeLoginNext(r), http.StatusFound)
}

type oidcProfile struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
}

var (
	errEmailUnverified       = errors.New("oidc provider did not assert a verified email")
	errPasswordAccountExists = errors.New("password account with this email exists")
)

func (a *API) upsertOIDCUser(ctx context.Context, p *oidcProfile) (*store.User, error) {
	verified := p.EmailVerified != nil && *p.EmailVerified
	if u, err := a.data.GetUserByGoogleSub(ctx, p.Sub); err == nil {
		if verified && normalizeEmail(p.Email) == u.Email {
			if err := a.redeemVerifiedEmailInvites(ctx, u); err != nil {
				return nil, err
			}
		}
		return u, nil
	}
	if !verified {
		return nil, errEmailUnverified
	}
	email := normalizeEmail(p.Email)
	if u, err := a.data.GetUserByEmail(ctx, email); err == nil {
		if u.GoogleSub != "" && u.GoogleSub != p.Sub {
			return nil, errEmailUnverified
		}
		if u.PasswordHash != "" {
			return nil, errPasswordAccountExists
		}
		u.GoogleSub = p.Sub
		if err := a.data.UpdateUser(ctx, u); err != nil {
			return nil, fmt.Errorf("link google account: %w", err)
		}
		if err := a.redeemVerifiedEmailInvites(ctx, u); err != nil {
			return nil, err
		}
		return u, nil
	}
	user := &store.User{ID: store.NewID("usr"), Email: email, Name: p.Name, GoogleSub: p.Sub, CreatedAt: a.now()}
	if a.hasLiveInviteFor(ctx, email) {
		if err := a.data.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		if err := a.redeemVerifiedEmailInvites(ctx, user); err != nil {
			return nil, err
		}
		return user, nil
	}
	return a.provisionNewUser(ctx, user)
}

func (c *authConfig) discover(ctx context.Context) (*oidcDiscovery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discovery != nil {
		return c.discovery, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.issuerURL()+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	var disc oidcDiscovery
	if err := fetchOIDCJSON(req, &disc); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if disc.TokenEndpoint == "" || disc.AuthorizationEndpoint == "" || disc.UserinfoEndpoint == "" {
		return nil, errors.New("incomplete oidc discovery document")
	}
	c.discovery = &disc
	return &disc, nil
}

func (c *authConfig) issuerURL() string {
	if issuer := strings.TrimRight(c.issuer, "/"); issuer != "" {
		return issuer
	}
	return googleIssuer
}

func (c *authConfig) issuerAccepted(iss string) bool {
	expected := c.issuerURL()
	if iss == expected {
		return true
	}
	return expected == googleIssuer && iss == googleIssuerBare
}

func fetchOIDCJSON(req *http.Request, dst any) error {
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxOIDCResponseBytes))
		return fmt.Errorf("%s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOIDCResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxOIDCResponseBytes {
		return fmt.Errorf("%s %s: response exceeds %d bytes", req.Method, req.URL.Path, maxOIDCResponseBytes)
	}
	return json.Unmarshal(body, dst)
}

func (a *API) clearOAuthCookies(w http.ResponseWriter) {
	for _, name := range []string{oauthStateCookie, oauthNonceCookie, oauthNextCookie} {
		a.expireCookie(w, name)
	}
}

type idTokenClaims struct {
	Iss   string          `json:"iss"`
	Exp   int64           `json:"exp"`
	Sub   string          `json:"sub"`
	Nonce string          `json:"nonce"`
	Aud   json.RawMessage `json:"aud"`
}

func parseIDTokenClaims(idToken string) (*idTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, fmt.Errorf("decode id_token: %w", err)
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse id_token: %w", err)
	}
	return &claims, nil
}

func audienceContains(raw json.RawMessage, clientID string) bool {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one == clientID
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return false
	}
	for _, a := range many {
		if a == clientID {
			return true
		}
	}
	return false
}

func (c *authConfig) verifyIDToken(idToken, nonce string, now time.Time) (*idTokenClaims, error) {
	if idToken == "" {
		return nil, errors.New("no id_token in response")
	}
	claims, err := parseIDTokenClaims(idToken)
	if err != nil {
		return nil, err
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("id_token nonce mismatch")
	}
	if !audienceContains(claims.Aud, c.googleClientID) {
		return nil, errors.New("id_token audience mismatch")
	}
	if !c.issuerAccepted(claims.Iss) {
		return nil, errors.New("id_token issuer mismatch")
	}
	if claims.Exp == 0 || !now.Before(time.Unix(claims.Exp, 0).Add(idTokenClockSkew)) {
		return nil, errors.New("id_token expired")
	}
	if claims.Sub == "" {
		return nil, errors.New("id_token missing subject")
	}
	return claims, nil
}

func (c *authConfig) exchangeAndProfile(ctx context.Context, disc *oidcDiscovery, code, nonce string, now time.Time) (*oidcProfile, error) {
	if code == "" {
		return nil, errors.New("missing code")
	}
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", c.googleClientID)
	form.Set("client_secret", c.googleClientSecret)
	form.Set("redirect_uri", c.redirectURL)
	form.Set("grant_type", "authorization_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, disc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := fetchOIDCJSON(req, &tok); err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("no access token in response")
	}
	claims, err := c.verifyIDToken(tok.IDToken, nonce, now)
	if err != nil {
		return nil, err
	}
	uReq, err := http.NewRequestWithContext(ctx, http.MethodGet, disc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	uReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	var profile oidcProfile
	if err := fetchOIDCJSON(uReq, &profile); err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	if profile.Sub == "" || profile.Email == "" {
		return nil, errors.New("incomplete userinfo")
	}
	if claims.Sub != profile.Sub {
		return nil, errors.New("id_token subject does not match userinfo")
	}
	return &profile, nil
}
