package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

const (
	maxTokenRequestBytes = 16 << 10
	accessTokenTTL       = time.Hour

	grantClientCredentials = "client_credentials"

	oauthInvalidRequest       = "invalid_request"
	oauthInvalidClient        = "invalid_client"
	oauthUnsupportedGrantType = "unsupported_grant_type"
	oauthServerError          = "server_error"
	oauthTemporarilyUnavail   = "temporarily_unavailable"
	oauthSlowDown             = "slow_down"
)

var unknownClientHash = store.HashToken("mishmesh-unknown-client")

func (a *API) registerOAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /oauth2/token", a.oauthTokenHandler)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", a.oauthMetadataHandler)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="mishmesh"`)
	}
	body := map[string]string{"error": code}
	if description != "" {
		body["error_description"] = description
	}
	writeJSON(w, status, body)
}

func writeTokenResponse(w http.ResponseWriter, issued identity.Issued, extra map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	body := map[string]any{
		"access_token": issued.Token,
		"token_type":   "Bearer",
		"expires_in":   int64(issued.ExpiresIn / time.Second),
	}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
}

type clientCredentials struct {
	id     string
	secret string
}

func readClientCredentials(r *http.Request) (clientCredentials, bool) {
	formID, formSecret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	basicID, basicSecret, hasBasic := r.BasicAuth()
	if hasBasic {
		if formSecret != "" {
			return clientCredentials{}, false
		}
		id, errID := url.QueryUnescape(basicID)
		secret, errSecret := url.QueryUnescape(basicSecret)
		if errID != nil || errSecret != nil || (formID != "" && formID != id) {
			return clientCredentials{}, false
		}
		return clientCredentials{id: id, secret: secret}, true
	}
	return clientCredentials{id: formID, secret: formSecret}, true
}

func (a *API) oauthTokenHandler(w http.ResponseWriter, r *http.Request) {
	if a.identity == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "token issuance not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenRequestBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "malformed form body")
		return
	}
	if len(r.URL.RawQuery) > 0 {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "parameters must be sent in the request body")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case grantClientCredentials:
		a.clientCredentialsGrant(w, r)
	case "":
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "grant_type required")
	default:
		a.extendedGrant(w, r)
	}
}

func (a *API) clientCredentialsGrant(w http.ResponseWriter, r *http.Request) {
	creds, ok := readClientCredentials(r)
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "use exactly one client authentication method")
		return
	}
	if creds.id == "" {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
		return
	}
	if !a.authRateAllowed(r, "client:"+creds.id) {
		w.Header().Set("Retry-After", "12")
		writeOAuthError(w, http.StatusTooManyRequests, oauthSlowDown, "too many attempts")
		return
	}
	sa, err := a.authenticateClient(r.Context(), creds)
	if errors.Is(err, errInvalidClient) {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
		return
	}
	if err != nil {
		a.log.Error("client credentials lookup failed", "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return
	}
	a.issueServiceAccountToken(w, r, sa, authz.AuthMethodClientCredentials, "")
}

var errInvalidClient = errors.New("invalid client")

func (a *API) authenticateClient(ctx context.Context, creds clientCredentials) (*store.ServiceAccount, error) {
	sa, err := a.data.GetServiceAccountByClientID(ctx, creds.id)
	if errors.Is(err, store.ErrNotFound) {
		store.ConstantTimeEqualHash(store.HashToken(creds.secret), unknownClientHash)
		return nil, errInvalidClient
	}
	if err != nil {
		return nil, err
	}
	if creds.secret == "" || !store.ConstantTimeEqualHash(store.HashToken(creds.secret), sa.SecretHash) || sa.Disabled {
		return nil, errInvalidClient
	}
	return sa, nil
}

func (a *API) issueServiceAccountToken(w http.ResponseWriter, r *http.Request, sa *store.ServiceAccount, method authz.AuthMethod, sessionID string) {
	issued, err := a.identity.Issue(r.Context(), identity.IssueRequest{
		SessionID:  sessionID,
		OrgID:      sa.OrgID,
		Subject:    authz.EntityRef{Type: authz.EntityServiceAccount, ID: sa.ID},
		Type:       token.TypeAccess,
		AuthMethod: method,
		TTL:        accessTokenTTL,
	})
	if err != nil {
		a.log.Error("issue service account token", "service_account", sa.ID, "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return
	}
	a.auditAs(r, sa.OrgID, "service_account:"+sa.ID, "oauth.token", sa.ID, string(method))
	writeTokenResponse(w, issued, nil)
}

func (a *API) extendedGrant(w http.ResponseWriter, _ *http.Request) {
	writeOAuthError(w, http.StatusBadRequest, oauthUnsupportedGrantType, "")
}

func (a *API) oauthMetadataHandler(w http.ResponseWriter, _ *http.Request) {
	if a.apiPublicURL == "" || a.identity == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                a.apiPublicURL,
		"token_endpoint":                        a.apiPublicURL + "/oauth2/token",
		"grant_types_supported":                 a.supportedGrants(),
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"response_types_supported":              []string{},
	})
}

func (a *API) supportedGrants() []string {
	return []string{grantClientCredentials}
}

func (a *API) auditAs(r *http.Request, orgID, actor, action, target, detail string) {
	ev := &store.AuditEvent{
		ID:        store.NewID("aud"),
		OrgID:     orgID,
		Actor:     actor,
		Action:    action,
		Target:    target,
		Detail:    detail,
		CreatedAt: a.now(),
	}
	if err := a.data.AppendAudit(r.Context(), ev); err != nil {
		a.log.Warn("append audit failed", "action", action, "err", err)
	}
}

func (a *API) requireMachine(action authz.Action, h http.HandlerFunc) http.HandlerFunc {
	human := a.require(action, h)
	return func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if a.identity == nil || !token.LooksLikeToken(raw) {
			human(w, r)
			return
		}
		ctx, ok := a.authorizeMachine(w, r, raw)
		if !ok {
			return
		}
		r = r.WithContext(ctx)
		if !a.permitted(r, action) {
			writeError(w, http.StatusForbidden, "permission denied: "+string(action))
			return
		}
		h(w, r)
	}
}

func (a *API) authorizeMachine(w http.ResponseWriter, r *http.Request, raw string) (context.Context, bool) {
	p, err := a.identity.Authenticate(r.Context(), raw, token.TypeAccess)
	if errors.Is(err, identity.ErrUnavailable) {
		writeStoreUnavailable(w)
		return nil, false
	}
	if err != nil || p.Ref.Type != authz.EntityServiceAccount {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	sa, err := a.data.GetServiceAccount(r.Context(), p.OrgID, p.Ref.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sa.Disabled) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	if err != nil {
		writeStoreUnavailable(w)
		return nil, false
	}
	ctx := a.authContext(r, p.OrgID, "service_account:"+sa.ID, sa.Role, false)
	return context.WithValue(ctx, ctxPrincipalType, authz.EntityServiceAccount), true
}
