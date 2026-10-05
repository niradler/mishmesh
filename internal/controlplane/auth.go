package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/store"
)

const sessionCookie = "mm_session"
const oauthStateCookie = "mm_oauth_state"

const (
	SignupModeOrg    = "org"
	SignupModeInvite = "invite"
)

type authConfig struct {
	enabled         bool
	passwordEnabled bool
	cookieSecure    bool
	sessionTTL      time.Duration
	signupMode      string

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
	SignupMode         string
	GoogleClientID     string
	GoogleClientSecret string
	RedirectURL        string
	Issuer             string
}

func (a *API) ConfigureAuth(opts AuthOptions) {
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 168 * time.Hour
	}
	if opts.SignupMode != SignupModeInvite {
		opts.SignupMode = SignupModeOrg
	}
	a.auth = &authConfig{
		signupMode:         opts.SignupMode,
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
	mux.HandleFunc("POST /api/v1/auth/switch-org", a.switchOrgHandler)
	mux.HandleFunc("POST /api/v1/auth/accept-invite", a.acceptInviteHandler)
	mux.HandleFunc("POST /api/v1/auth/register", a.registerHandler)
	mux.HandleFunc("GET /api/v1/auth/google/start", a.googleStartHandler)
	mux.HandleFunc("GET /api/v1/auth/google/callback", a.googleCallbackHandler)
}

func (a *API) authConfigHandler(w http.ResponseWriter, _ *http.Request) {
	mode := SignupModeOrg
	if a.auth != nil {
		mode = a.auth.signupMode
	}
	passwordEnabled := a.auth != nil && a.auth.passwordEnabled
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_enabled":     a.authEnabled(),
		"password_enabled": passwordEnabled,
		"password_signup":  a.authEnabled() && passwordEnabled,
		"google_enabled":   a.googleEnabled(),
		"signup_mode":      mode,
	})
}

type sessionUser struct {
	user  *store.User
	orgID string
	role  string
}

var errNoSession = errors.New("no valid session")

func (a *API) resolveSession(r *http.Request) (*sessionUser, bool) {
	su, err := a.lookupSession(r)
	return su, err == nil
}

func (a *API) lookupSession(r *http.Request) (*sessionUser, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, errNoSession
	}
	sess, err := a.data.GetSession(r.Context(), store.HashToken(c.Value))
	if err != nil {
		return nil, sessionLookupErr(err)
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = a.data.DeleteSession(r.Context(), sess.IDHash)
		return nil, errNoSession
	}
	user, err := a.data.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		return nil, sessionLookupErr(err)
	}
	m, err := a.data.GetMembership(r.Context(), sess.OrgID, user.ID)
	if err != nil {
		return nil, sessionLookupErr(err)
	}
	return &sessionUser{user: user, orgID: sess.OrgID, role: m.Role}, nil
}

func sessionLookupErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errNoSession
	}
	return err
}

func writeStoreUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusServiceUnavailable, "temporarily unavailable, retry shortly")
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
	email := normalizeEmail(req.Email)
	if !a.authRateAllowed(r, email) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	user, err := a.data.GetUserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeStoreUnavailable(w)
		return
	}
	if err != nil || user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	orgID, ok := a.primaryOrg(r.Context(), user.ID)
	if !ok {
		writeError(w, http.StatusForbidden, "no organization membership")
		return
	}
	if err := a.issueSession(r.Context(), w, user.ID, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	a.writeMe(w, r, http.StatusOK, user, orgID)
}

func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || !a.auth.passwordEnabled {
		writeError(w, http.StatusForbidden, "password registration disabled")
		return
	}
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		Name        string `json:"name"`
		InviteToken string `json:"invite_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := normalizeEmail(req.Email)
	if !a.authRateAllowed(r, email) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "valid email required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password (>=8 chars) required")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash failed")
		return
	}
	user, err := a.registerPasswordUser(r.Context(), email, req.Name, string(hash), req.InviteToken)
	switch {
	case errors.Is(err, errEmailTaken):
		writeError(w, http.StatusConflict, "email already registered")
		return
	case errors.Is(err, errInvalidInvite):
		writeError(w, http.StatusForbidden, "invalid or expired invite")
		return
	case errors.Is(err, errSignupClosed):
		writeError(w, http.StatusForbidden, "registration is by invitation only")
		return
	case err != nil:
		a.log.Warn("register failed", "err", err)
		writeError(w, http.StatusInternalServerError, "register failed")
		return
	}
	orgID, ok := a.primaryOrg(r.Context(), user.ID)
	if !ok {
		writeError(w, http.StatusForbidden, "no organization membership")
		return
	}
	if err := a.issueSession(r.Context(), w, user.ID, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	a.writeMe(w, r, http.StatusCreated, user, orgID)
}

func (a *API) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = a.data.DeleteSession(r.Context(), store.HashToken(c.Value))
	}
	a.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type membershipDTO struct {
	OrgID   string `json:"org_id"`
	OrgName string `json:"org_name"`
	Role    string `json:"role"`
}

type meDTO struct {
	ID          string          `json:"id"`
	Email       string          `json:"email"`
	Name        string          `json:"name"`
	ActiveOrgID string          `json:"active_org_id"`
	Role        string          `json:"role"`
	Memberships []membershipDTO `json:"memberships"`
}

func (a *API) writeMe(w http.ResponseWriter, r *http.Request, status int, user *store.User, activeOrgID string) {
	memberships, err := a.data.ListMembershipsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	dto := meDTO{ID: user.ID, Email: user.Email, Name: user.Name, ActiveOrgID: activeOrgID, Memberships: make([]membershipDTO, 0, len(memberships))}
	for _, m := range memberships {
		name := m.OrgID
		if org, err := a.data.GetOrg(r.Context(), m.OrgID); err == nil {
			name = org.Name
		}
		if m.OrgID == activeOrgID {
			dto.Role = m.Role
		}
		dto.Memberships = append(dto.Memberships, membershipDTO{OrgID: m.OrgID, OrgName: name, Role: m.Role})
	}
	writeJSON(w, status, dto)
}

func (a *API) meHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.resolveSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	a.writeMe(w, r, http.StatusOK, su.user, su.orgID)
}

func (a *API) switchOrgHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.resolveSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		OrgID string `json:"org_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := a.data.GetMembership(r.Context(), req.OrgID, su.user.ID); err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.data.DeleteSession(r.Context(), store.HashToken(c.Value))
	}
	if err := a.issueSession(r.Context(), w, su.user.ID, req.OrgID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	a.writeMe(w, r, http.StatusOK, su.user, req.OrgID)
}

func (a *API) primaryOrg(ctx context.Context, userID string) (string, bool) {
	memberships, err := a.data.ListMembershipsByUser(ctx, userID)
	if err != nil || len(memberships) == 0 {
		return "", false
	}
	return memberships[0].OrgID, true
}

var (
	errEmailTaken   = errors.New("email already registered")
	errSignupClosed = errors.New("signup closed")
)

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validEmail(email string) bool {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return false
	}
	at := strings.LastIndex(email, "@")
	return at > 0 && strings.Contains(email[at+1:], ".") && !strings.HasSuffix(email, ".")
}

func (a *API) registerPasswordUser(ctx context.Context, email, name, hash, inviteToken string) (*store.User, error) {
	_, err := a.data.GetUserByEmail(ctx, email)
	if err == nil {
		return nil, errEmailTaken
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	user := &store.User{ID: store.NewID("usr"), Email: email, Name: name, PasswordHash: hash, CreatedAt: time.Now()}
	if inviteToken == "" {
		return a.provisionNewUser(ctx, user)
	}
	inv, err := a.lookupInvite(ctx, inviteToken, email)
	if err != nil {
		return nil, err
	}
	if err := a.claimInvite(ctx, inv); err != nil {
		return nil, err
	}
	if err := a.data.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	if err := a.grantInviteMembership(ctx, user.ID, inv); err != nil {
		return nil, err
	}
	return user, nil
}

func (a *API) provisionNewUser(ctx context.Context, user *store.User) (*store.User, error) {
	if a.auth.signupMode == SignupModeInvite {
		if a.hasDefaultOwner(ctx) {
			return nil, errSignupClosed
		}
		if err := a.data.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		if err := a.joinDefaultOrg(ctx, user.ID, store.RoleOwner); err != nil {
			return nil, err
		}
		return user, nil
	}
	if err := a.data.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	org, err := a.createOrg(ctx, orgNameFor(user))
	if err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}
	if err := a.data.CreateMembership(ctx, &store.Membership{OrgID: org.ID, UserID: user.ID, Role: store.RoleOwner, CreatedAt: time.Now()}); err != nil {
		return nil, fmt.Errorf("create membership: %w", err)
	}
	return user, nil
}

func (a *API) hasDefaultOwner(ctx context.Context) bool {
	ms, err := a.data.ListMembershipsByOrg(ctx, defaultOrgID)
	if err != nil {
		return true
	}
	for _, m := range ms {
		if m.Role == store.RoleOwner {
			return true
		}
	}
	return false
}

func (a *API) joinDefaultOrg(ctx context.Context, userID, role string) error {
	org, err := a.ensureOrg(ctx, defaultOrgID)
	if err != nil {
		return fmt.Errorf("ensure default org: %w", err)
	}
	if err := a.data.CreateMembership(ctx, &store.Membership{OrgID: org.ID, UserID: userID, Role: role, CreatedAt: time.Now()}); err != nil {
		return fmt.Errorf("create membership: %w", err)
	}
	return nil
}

func orgNameFor(u *store.User) string {
	if n := strings.TrimSpace(u.Name); n != "" {
		return n
	}
	if at := strings.Index(u.Email, "@"); at > 0 {
		return u.Email[:at]
	}
	return u.Email
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
	if err := a.issueSession(r.Context(), w, user.ID, orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
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
			if _, err := a.redeemVerifiedEmailInvites(ctx, u); err != nil {
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
		_ = a.data.UpdateUser(ctx, u)
		if _, err := a.redeemVerifiedEmailInvites(ctx, u); err != nil {
			return nil, err
		}
		return u, nil
	}
	user := &store.User{ID: store.NewID("usr"), Email: email, Name: p.Name, GoogleSub: p.Sub, CreatedAt: time.Now()}
	if a.hasLiveInviteFor(ctx, email) {
		if err := a.data.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		if _, err := a.redeemVerifiedEmailInvites(ctx, user); err != nil {
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
