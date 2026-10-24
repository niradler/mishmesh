package ingress

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

const endpointSessionCookie = "mm_ep_session"

func (i *Ingress) endpointSessionPrincipal(r *http.Request, ep *store.Endpoint) (*identity.Principal, error) {
	c, err := r.Cookie(endpointSessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	p, err := i.identity.Authenticate(r.Context(), c.Value, token.TypeSession)
	if errors.Is(err, identity.ErrUnavailable) {
		return nil, err
	}
	if err != nil || p.EndpointID != ep.ID || p.OrgID != ep.OrgID || p.Ref.Type != authz.EntityUser {
		return nil, nil
	}
	return &p, nil
}

func (i *Ingress) canRedirectToLogin(r *http.Request, idp *store.EndpointIdentity) bool {
	if !idp.BrowserLogin || i.apiURL == "" {
		return false
	}
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

func (i *Ingress) redirectToLogin(w http.ResponseWriter, r *http.Request, ep *store.Endpoint, force bool) {
	q := url.Values{"endpoint": {ep.ID}, "return": {r.URL.RequestURI()}}
	if force {
		q.Set("force", "1")
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, i.apiURL+identity.EndpointLoginPath+"?"+q.Encode(), http.StatusFound)
	i.recordCode(http.StatusFound)
}

func endpointCookiePath(r *http.Request, ep *store.Endpoint) string {
	prefix := "/tunnel/" + ep.ID
	if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
		return prefix
	}
	return "/"
}

func (i *Ingress) handleEndpointCallback(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) {
	if ep.Policy == nil || ep.Policy.Identity == nil || !ep.Policy.Identity.BrowserLogin {
		i.writeResolveError(w, errTunnelNotFound)
		return
	}
	if i.identity == nil {
		writeIdentityUnavailable(w)
		i.recordCode(http.StatusServiceUnavailable)
		return
	}
	login, err := i.identity.TakeEndpointLogin(r.Context(), r.URL.Query().Get("code"))
	if errors.Is(err, identity.ErrUnavailable) {
		i.log.Error("endpoint login lookup failed", "endpoint_id", ep.ID, "err", err)
		writeIdentityUnavailable(w)
		i.recordCode(http.StatusServiceUnavailable)
		return
	}
	if err != nil || login.EndpointID != ep.ID || login.OrgID != ep.OrgID {
		http.Error(w, "login expired, retry", http.StatusUnauthorized)
		i.recordCode(http.StatusUnauthorized)
		return
	}
	issued, err := i.identity.Issue(r.Context(), identity.IssueRequest{
		OrgID:      login.OrgID,
		Subject:    authz.EntityRef{Type: authz.EntityUser, ID: login.UserID},
		Type:       token.TypeSession,
		AuthMethod: login.AuthMethod,
		AuthTime:   login.AuthTime,
		TTL:        identity.EndpointSessionTTL,
		EndpointID: ep.ID,
	})
	if err != nil {
		i.log.Error("endpoint session issue failed", "endpoint_id", ep.ID, "err", err)
		writeIdentityUnavailable(w)
		i.recordCode(http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     endpointSessionCookie,
		Value:    issued.Token,
		Path:     endpointCookiePath(r, ep),
		HttpOnly: true,
		Secure:   i.secure || requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  issued.Session.ExpiresAt,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, login.Return, http.StatusFound)
	i.recordCode(http.StatusFound)
}
