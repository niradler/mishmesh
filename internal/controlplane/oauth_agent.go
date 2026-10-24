package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	grantAuthorizationCode = "authorization_code"
	grantRefreshToken      = "refresh_token"
	grantDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"

	oauthInvalidGrant         = "invalid_grant"
	oauthAuthorizationPending = "authorization_pending"
	oauthExpiredToken         = "expired_token"
	oauthUnauthorizedClient   = "unauthorized_client"
	oauthAccessDenied         = "access_denied"

	deviceVerificationPath = "/device"
	consentPath            = "/consent"
)

func (a *API) registerAgentOAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth2/authorize", a.oauthAuthorizeHandler)
	mux.HandleFunc("POST /oauth2/device_authorization", a.deviceAuthorizationHandler)
	mux.HandleFunc("POST /oauth2/revoke", a.oauthRevokeHandler)
	mux.HandleFunc("GET /api/v1/auth/device", a.deviceLookupHandler)
	mux.HandleFunc("POST /api/v1/auth/device/approve", a.csrfGuarded(a.deviceApproveHandler))
	mux.HandleFunc("POST /api/v1/auth/device/deny", a.csrfGuarded(a.deviceDenyHandler))
	mux.HandleFunc("GET /api/v1/auth/consent/{id}", a.consentLookupHandler)
	mux.HandleFunc("POST /api/v1/auth/consent/{id}", a.csrfGuarded(a.consentDecideHandler))
}

func (a *API) agentLoginOrg(ctx context.Context, su *sessionUser, requested string) (string, error) {
	orgID := su.orgID
	if requested != "" {
		orgID = requested
	}
	m, err := a.data.GetMembership(ctx, orgID, su.user.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.DisabledAt != nil) {
		return "", store.ErrNotFound
	}
	return orgID, err
}

func (a *API) oauthAuthorizeHandler(w http.ResponseWriter, r *http.Request) {
	if a.identity == nil || !a.authEnabled() {
		writeError(w, http.StatusServiceUnavailable, "agent login unavailable")
		return
	}
	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	if q.Get("client_id") != identity.AgentClientID || !identity.LoopbackRedirect(redirectURI) {
		writeError(w, http.StatusBadRequest, "unknown client or redirect_uri")
		return
	}
	fail := func(code string) {
		v := url.Values{"error": {code}}
		if s := q.Get("state"); s != "" {
			v.Set("state", s)
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, redirectURI+"?"+v.Encode(), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type")
		return
	}
	if !identity.ValidState(q.Get("state")) || q.Get("code_challenge_method") != "S256" || !identity.ValidCodeChallenge(q.Get("code_challenge")) {
		fail(oauthInvalidRequest)
		return
	}
	su, err := a.lookupSession(r)
	if errors.Is(err, errNoSession) {
		a.loginRedirect(w, r)
		return
	}
	if err != nil {
		writeStoreUnavailable(w)
		return
	}
	orgID, err := a.agentLoginOrg(r.Context(), su, q.Get("org_id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(oauthAccessDenied)
		return
	}
	if err != nil {
		writeStoreUnavailable(w)
		return
	}
	consentID, err := a.identity.StartConsent(r.Context(), identity.AgentLogin{
		OrgID:       orgID,
		UserID:      su.user.ID,
		AuthMethod:  su.authMethod,
		AuthTime:    su.authTime,
		Challenge:   q.Get("code_challenge"),
		RedirectURI: redirectURI,
	}, q.Get("state"))
	if err != nil {
		a.log.Error("agent consent request failed", "err", err)
		writeStoreUnavailable(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, consentPath+"?"+url.Values{"request": {consentID}}.Encode(), http.StatusFound)
}

func (a *API) agentLoginSession(w http.ResponseWriter, r *http.Request, guessable bool) (*sessionUser, bool) {
	if a.identity == nil || !a.authEnabled() {
		writeError(w, http.StatusServiceUnavailable, "agent login unavailable")
		return nil, false
	}
	su, err := a.lookupSession(r)
	if errors.Is(err, errNoSession) {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	if err != nil {
		writeStoreUnavailable(w)
		return nil, false
	}
	rateKey := ""
	if guessable {
		rateKey = "device:" + su.user.ID
	}
	if !a.authRateAllowed(r, rateKey) {
		w.Header().Set("Retry-After", "12")
		writeError(w, http.StatusTooManyRequests, "too many attempts")
		return nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return su, true
}

func (a *API) consentLookupHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.agentLoginSession(w, r, false)
	if !ok {
		return
	}
	req, err := a.identity.LookupConsent(r.Context(), r.PathValue("id"), su.user.ID)
	if a.agentLoginLookupFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           req.ID,
		"client_id":    req.ClientID,
		"org_id":       req.OrgID,
		"redirect_uri": req.RedirectURI,
		"auth_method":  string(req.AuthMethod),
		"expires_at":   req.ExpiresAt,
	})
}

func (a *API) consentDecideHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.agentLoginSession(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Approve *bool `json:"approve"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Approve == nil {
		writeError(w, http.StatusBadRequest, "approve is required")
		return
	}
	id := r.PathValue("id")
	if *body.Approve {
		req, err := a.identity.LookupConsent(r.Context(), id, su.user.ID)
		if a.agentLoginLookupFailed(w, err) {
			return
		}
		if _, err := a.agentLoginOrg(r.Context(), su, req.OrgID); a.agentLoginLookupFailed(w, err) {
			return
		}
	}
	redirect, req, err := a.identity.DecideConsent(r.Context(), id, su.user.ID, *body.Approve)
	if a.agentLoginLookupFailed(w, err) {
		return
	}
	action := "oauth.consent_deny"
	if *body.Approve {
		action = "oauth.authorize"
	}
	a.auditAs(r, req.OrgID, su.user.Email, action, identity.AgentClientID, string(su.authMethod))
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(w, http.StatusOK, map[string]any{"redirect_to": redirect})
}

func (a *API) agentLoginLookupFailed(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, identity.ErrUnauthenticated), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "unknown or expired request")
	default:
		a.log.Error("agent login request failed", "err", err)
		writeStoreUnavailable(w)
	}
	return true
}

func (a *API) deviceLookupHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.agentLoginSession(w, r, true); !ok {
		return
	}
	req, err := a.identity.LookupDevice(r.Context(), r.URL.Query().Get("user_code"))
	if a.agentLoginLookupFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_code":  req.UserCode,
		"client_id":  req.ClientID,
		"expires_at": req.ExpiresAt,
	})
}

func (a *API) deviceDenyHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.agentLoginSession(w, r, true)
	if !ok {
		return
	}
	var req struct {
		UserCode string `json:"user_code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if a.agentLoginLookupFailed(w, a.identity.DenyDevice(r.Context(), req.UserCode)) {
		return
	}
	a.auditAs(r, su.orgID, su.user.Email, "oauth.device_deny", identity.AgentClientID, string(su.authMethod))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deviceAuthorizationHandler(w http.ResponseWriter, r *http.Request) {
	if a.identity == nil || !a.authEnabled() {
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "agent login unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenRequestBytes)
	if err := r.ParseForm(); err != nil || len(r.URL.RawQuery) > 0 {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "parameters must be sent in the request body")
		return
	}
	if r.PostForm.Get("client_id") != identity.AgentClientID {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "")
		return
	}
	if !a.authRateAllowed(r, "") {
		w.Header().Set("Retry-After", "3")
		writeOAuthError(w, http.StatusTooManyRequests, oauthSlowDown, "too many attempts")
		return
	}
	start, err := a.identity.StartDevice(r.Context())
	if err != nil {
		a.log.Error("device authorization failed", "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return
	}
	verification := a.apiPublicURL + deviceVerificationPath
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               start.DeviceCode,
		"user_code":                 start.UserCode,
		"verification_uri":          verification,
		"verification_uri_complete": verification + "?" + url.Values{"user_code": {start.UserCode}}.Encode(),
		"expires_in":                int64(start.ExpiresIn / time.Second),
		"interval":                  int64(start.Interval / time.Second),
	})
}

func (a *API) deviceApproveHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.agentLoginSession(w, r, true)
	if !ok {
		return
	}
	var req struct {
		UserCode string `json:"user_code"`
		OrgID    string `json:"org_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	orgID, err := a.agentLoginOrg(r.Context(), su, req.OrgID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeStoreUnavailable(w)
		return
	}
	err = a.identity.ApproveDevice(r.Context(), req.UserCode, identity.AgentLogin{
		OrgID:      orgID,
		UserID:     su.user.ID,
		AuthMethod: su.authMethod,
		AuthTime:   su.authTime,
	})
	if errors.Is(err, identity.ErrUnauthenticated) {
		writeError(w, http.StatusNotFound, "unknown or expired code")
		return
	}
	if err != nil {
		writeStoreUnavailable(w)
		return
	}
	a.auditAs(r, orgID, su.user.Email, "oauth.device_approve", identity.AgentClientID, string(su.authMethod))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) agentGrant(w http.ResponseWriter, r *http.Request) {
	if r.PostForm.Get("client_id") != identity.AgentClientID || r.PostForm.Get("client_secret") != "" || r.Header.Get("Authorization") != "" {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "")
		return
	}
	if !a.authEnabled() {
		writeOAuthError(w, http.StatusBadRequest, oauthUnauthorizedClient, "agent login unavailable")
		return
	}
	ctx := r.Context()
	switch r.PostForm.Get("grant_type") {
	case grantAuthorizationCode:
		if !a.authRateAllowed(r, "") {
			writeOAuthError(w, http.StatusTooManyRequests, oauthSlowDown, "too many attempts")
			return
		}
		login, err := a.identity.RedeemAuthCode(ctx, r.PostForm.Get("code"), r.PostForm.Get("code_verifier"), r.PostForm.Get("redirect_uri"))
		a.finishAgentLogin(w, r, login, err)
	case grantDeviceCode:
		login, err := a.identity.PollDevice(ctx, r.PostForm.Get("device_code"))
		a.finishAgentLogin(w, r, login, err)
	case grantRefreshToken:
		if !a.authRateAllowed(r, "") {
			writeOAuthError(w, http.StatusTooManyRequests, oauthSlowDown, "too many attempts")
			return
		}
		tokens, err := a.identity.RefreshAgentUser(ctx, r.PostForm.Get("refresh_token"))
		if err == nil {
			err = a.agentUserActive(ctx, tokens.Session.OrgID, tokens.Session.SubjectID)
			if errors.Is(err, identity.ErrUnauthenticated) {
				_ = a.identity.RevokeSession(ctx, tokens.Session.ID)
			}
		}
		if a.agentGrantFailed(w, err) {
			return
		}
		a.auditAs(r, tokens.Session.OrgID, "user:"+tokens.Session.SubjectID, "oauth.token", tokens.Session.SubjectID, grantRefreshToken)
		writeAgentTokens(w, tokens)
	}
}

func (a *API) agentUserActive(ctx context.Context, orgID, userID string) error {
	m, err := a.data.GetMembership(ctx, orgID, userID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.DisabledAt != nil) {
		return identity.ErrUnauthenticated
	}
	if err != nil {
		return identity.ErrUnavailable
	}
	return nil
}

func (a *API) finishAgentLogin(w http.ResponseWriter, r *http.Request, login identity.AgentLogin, err error) {
	if err == nil {
		err = a.agentUserActive(r.Context(), login.OrgID, login.UserID)
	}
	if a.agentGrantFailed(w, err) {
		return
	}
	tokens, err := a.identity.IssueAgentUser(r.Context(), login)
	if a.agentGrantFailed(w, err) {
		return
	}
	a.auditAs(r, login.OrgID, "user:"+login.UserID, "oauth.token", login.UserID, r.PostForm.Get("grant_type"))
	writeAgentTokens(w, tokens)
}

func (a *API) agentGrantFailed(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, identity.ErrAuthorizationPending):
		writeOAuthError(w, http.StatusBadRequest, oauthAuthorizationPending, "")
	case errors.Is(err, identity.ErrSlowDown):
		writeOAuthError(w, http.StatusBadRequest, oauthSlowDown, "")
	case errors.Is(err, identity.ErrExpiredCode):
		writeOAuthError(w, http.StatusBadRequest, oauthExpiredToken, "")
	case errors.Is(err, identity.ErrAccessDenied):
		writeOAuthError(w, http.StatusBadRequest, oauthAccessDenied, "")
	case errors.Is(err, identity.ErrUnauthenticated):
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidGrant, "")
	default:
		a.log.Error("agent token grant failed", "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
	}
	return true
}

func writeAgentTokens(w http.ResponseWriter, tokens identity.AgentTokens) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"token_type":    "Bearer",
		"expires_in":    int64(tokens.ExpiresIn / time.Second),
		"org_id":        tokens.Session.OrgID,
	})
}

func (a *API) oauthRevokeHandler(w http.ResponseWriter, r *http.Request) {
	if a.identity == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenRequestBytes)
	if err := r.ParseForm(); err != nil || len(r.URL.RawQuery) > 0 {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "parameters must be sent in the request body")
		return
	}
	if r.PostForm.Get("client_id") != identity.AgentClientID {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "")
		return
	}
	if err := a.identity.RevokeToken(r.Context(), r.PostForm.Get("token")); err != nil {
		a.log.Error("token revocation failed", "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
