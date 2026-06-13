package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/store"
)

const sessionCookie = "mm_session"
const oauthStateCookie = "mm_oauth_state"

type authConfig struct {
	enabled         bool
	passwordEnabled bool
	cookieSecure    bool
	sessionTTL      time.Duration

	googleClientID     string
	googleClientSecret string
	redirectURL        string
	issuer             string

	mu        sync.Mutex
	discovery *oidcDiscovery
}

type oidcDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

type AuthOptions struct {
	Enabled            bool
	PasswordEnabled    bool
	CookieSecure       bool
	SessionTTL         time.Duration
	GoogleClientID     string
	GoogleClientSecret string
	RedirectURL        string
	Issuer             string
}

func (a *API) ConfigureAuth(opts AuthOptions) {
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 168 * time.Hour
	}
	a.auth = &authConfig{
		enabled:            opts.Enabled,
		passwordEnabled:    opts.PasswordEnabled,
		cookieSecure:       opts.CookieSecure,
		sessionTTL:         opts.SessionTTL,
		googleClientID:     opts.GoogleClientID,
		googleClientSecret: opts.GoogleClientSecret,
		redirectURL:        opts.RedirectURL,
		issuer:             opts.Issuer,
	}
}

func (a *API) authEnabled() bool { return a.auth != nil && a.auth.enabled }

func (a *API) googleEnabled() bool {
	return a.auth != nil && a.auth.googleClientID != "" && a.auth.googleClientSecret != "" && a.auth.redirectURL != ""
}

func (a *API) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/config", a.authConfigHandler)
	mux.HandleFunc("POST /api/v1/auth/login", a.loginHandler)
	mux.HandleFunc("POST /api/v1/auth/logout", a.logoutHandler)
	mux.HandleFunc("GET /api/v1/auth/me", a.meHandler)
	mux.HandleFunc("POST /api/v1/auth/register", a.registerHandler)
	mux.HandleFunc("GET /api/v1/auth/google/start", a.googleStartHandler)
	mux.HandleFunc("GET /api/v1/auth/google/callback", a.googleCallbackHandler)
}

func (a *API) authConfigHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"auth_enabled":     a.authEnabled(),
		"password_enabled": a.auth != nil && a.auth.passwordEnabled,
		"google_enabled":   a.googleEnabled(),
	})
}

type sessionUser struct {
	user  *store.User
	orgID string
	role  string
}

func (a *API) resolveSession(r *http.Request) (*sessionUser, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	sess, err := a.data.GetSession(r.Context(), store.HashToken(c.Value))
	if err != nil {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = a.data.DeleteSession(r.Context(), sess.IDHash)
		return nil, false
	}
	user, err := a.data.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		return nil, false
	}
	role := store.RoleMember
	if m, err := a.data.GetMembership(r.Context(), sess.OrgID, user.ID); err == nil {
		role = m.Role
	}
	return &sessionUser{user: user, orgID: sess.OrgID, role: role}, true
}

func (a *API) issueSession(ctx context.Context, w http.ResponseWriter, userID, orgID string) error {
	raw, err := randomToken()
	if err != nil {
		return err
	}
	now := time.Now()
	sess := &store.Session{
		IDHash:    store.HashToken(raw),
		UserID:    userID,
		OrgID:     orgID,
		CreatedAt: now,
		ExpiresAt: now.Add(a.auth.sessionTTL),
	}
	if err := a.data.CreateSession(ctx, sess); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.auth.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})
	return nil
}

func (a *API) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.auth.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (a *API) loginHandler(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || !a.auth.passwordEnabled {
		writeError(w, http.StatusForbidden, "password login disabled")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := a.data.GetUserByEmail(r.Context(), req.Email)
	if err != nil || user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	orgID, role := a.primaryOrg(r.Context(), user.ID)
	if err := a.issueSession(r.Context(), w, user.ID, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userDTO(user), "org": orgID, "role": role})
}

func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || !a.auth.passwordEnabled {
		writeError(w, http.StatusForbidden, "password registration disabled")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Email == "" || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "email and password (>=8 chars) required")
		return
	}
	if _, err := a.data.GetUserByEmail(r.Context(), req.Email); err == nil {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash failed")
		return
	}
	user := &store.User{ID: store.NewID("usr"), Email: req.Email, Name: req.Name, PasswordHash: string(hash), CreatedAt: time.Now()}
	if err := a.data.CreateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "create user failed")
		return
	}
	orgID := a.bootstrapMembership(r.Context(), user.ID)
	if err := a.issueSession(r.Context(), w, user.ID, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": userDTO(user), "org": orgID})
}

func (a *API) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = a.data.DeleteSession(r.Context(), store.HashToken(c.Value))
	}
	a.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) meHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.resolveSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	memberships, _ := a.data.ListMembershipsByUser(r.Context(), su.user.ID)
	mems := make([]map[string]string, 0, len(memberships))
	for _, m := range memberships {
		mems = append(mems, map[string]string{"org": m.OrgID, "role": m.Role})
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userDTO(su.user), "org": su.orgID, "role": su.role, "memberships": mems})
}

func (a *API) primaryOrg(ctx context.Context, userID string) (string, string) {
	memberships, err := a.data.ListMembershipsByUser(ctx, userID)
	if err != nil || len(memberships) == 0 {
		return a.bootstrapMembership(ctx, userID), store.RoleOwner
	}
	return memberships[0].OrgID, memberships[0].Role
}

func (a *API) bootstrapMembership(ctx context.Context, userID string) string {
	org, err := a.ensureOrg(ctx, defaultOrgID)
	if err != nil {
		return defaultOrgID
	}
	role := store.RoleMember
	if n, err := a.data.CountUsers(ctx); err == nil && n <= 1 {
		role = store.RoleOwner
	}
	_ = a.data.CreateMembership(ctx, &store.Membership{OrgID: org.ID, UserID: userID, Role: role, CreatedAt: time.Now()})
	return org.ID
}

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
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: state, Path: "/", HttpOnly: true, Secure: a.auth.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	q := url.Values{}
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
	if err != nil || state == "" || c.Value != state {
		writeError(w, http.StatusBadRequest, "invalid oauth state")
		return
	}
	disc, err := a.auth.discover(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "oidc discovery failed")
		return
	}
	profile, err := a.auth.exchangeAndProfile(r.Context(), disc, r.URL.Query().Get("code"))
	if err != nil {
		a.log.Warn("google oauth exchange failed", "err", err)
		writeError(w, http.StatusUnauthorized, "oauth exchange failed")
		return
	}
	user, err := a.upsertOIDCUser(r.Context(), profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user upsert failed")
		return
	}
	orgID, _ := a.primaryOrg(r.Context(), user.ID)
	if err := a.issueSession(r.Context(), w, user.ID, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

type oidcProfile struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (a *API) upsertOIDCUser(ctx context.Context, p *oidcProfile) (*store.User, error) {
	if u, err := a.data.GetUserByGoogleSub(ctx, p.Sub); err == nil {
		return u, nil
	}
	if u, err := a.data.GetUserByEmail(ctx, p.Email); err == nil {
		u.GoogleSub = p.Sub
		_ = a.data.UpdateUser(ctx, u)
		return u, nil
	}
	user := &store.User{ID: store.NewID("usr"), Email: p.Email, Name: p.Name, GoogleSub: p.Sub, CreatedAt: time.Now()}
	if err := a.data.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	a.bootstrapMembership(ctx, user.ID)
	return user, nil
}

func (c *authConfig) discover(ctx context.Context) (*oidcDiscovery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discovery != nil {
		return c.discovery, nil
	}
	issuer := strings.TrimRight(c.issuer, "/")
	if issuer == "" {
		issuer = "https://accounts.google.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var disc oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		return nil, err
	}
	if disc.TokenEndpoint == "" || disc.AuthorizationEndpoint == "" {
		return nil, errors.New("incomplete oidc discovery document")
	}
	c.discovery = &disc
	return &disc, nil
}

func (c *authConfig) exchangeAndProfile(ctx context.Context, disc *oidcDiscovery, code string) (*oidcProfile, error) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, err
	}
	if tok.AccessToken == "" {
		return nil, errors.New("no access token in response")
	}
	uReq, err := http.NewRequestWithContext(ctx, http.MethodGet, disc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	uReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uResp, err := http.DefaultClient.Do(uReq)
	if err != nil {
		return nil, err
	}
	defer uResp.Body.Close()
	var profile oidcProfile
	if err := json.NewDecoder(uResp.Body).Decode(&profile); err != nil {
		return nil, err
	}
	if profile.Sub == "" || profile.Email == "" {
		return nil, errors.New("incomplete userinfo")
	}
	return &profile, nil
}

func userDTO(u *store.User) map[string]string {
	return map[string]string{"id": u.ID, "email": u.Email, "name": u.Name}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
