package controlplane

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

func localNext(next string) bool {
	return identity.SafeReturnPath(next)
}

func (a *API) takeLoginNext(r *http.Request) string {
	c, err := r.Cookie(oauthNextCookie)
	if err != nil || !localNext(c.Value) {
		return "/"
	}
	return c.Value
}

func (a *API) loginRedirect(w http.ResponseWriter, r *http.Request) {
	next := r.URL.RequestURI()
	if a.googleEnabled() {
		http.Redirect(w, r, "/api/v1/auth/google/start?"+url.Values{"next": {next}}.Encode(), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login?"+url.Values{"next": {next}}.Encode(), http.StatusFound)
}

func (a *API) endpointLoginHandler(w http.ResponseWriter, r *http.Request) {
	if a.identity == nil || !a.authEnabled() {
		writeError(w, http.StatusServiceUnavailable, "endpoint login unavailable")
		return
	}
	q := r.URL.Query()
	returnPath := q.Get("return")
	if returnPath == "" {
		returnPath = "/"
	}
	if !identity.SafeReturnPath(returnPath) {
		writeError(w, http.StatusBadRequest, "invalid return path")
		return
	}
	ep, err := a.data.GetEndpoint(r.Context(), q.Get("endpoint"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !browserLoginEndpoint(ep)) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeStoreUnavailable(w)
		return
	}
	target := a.publicURL(ep)
	if target == "" {
		writeError(w, http.StatusNotFound, "not found")
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
	if q.Get("force") == "1" && a.now().Sub(su.authTime) > identity.EndpointReauthMaxAge {
		_ = a.data.DeleteSession(r.Context(), su.idHash)
		a.clearSessionCookie(w)
		a.loginRedirect(w, r)
		return
	}
	m, err := a.data.GetMembership(r.Context(), ep.OrgID, su.user.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && m.DisabledAt != nil) {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	if err != nil {
		writeStoreUnavailable(w)
		return
	}
	code, err := a.identity.PutEndpointLogin(r.Context(), identity.EndpointLogin{
		EndpointID: ep.ID,
		OrgID:      ep.OrgID,
		UserID:     su.user.ID,
		AuthMethod: su.authMethod,
		AuthTime:   su.authTime,
		Return:     returnPath,
	})
	if err != nil {
		a.log.Error("endpoint login code failed", "endpoint", ep.ID, "err", err)
		writeStoreUnavailable(w)
		return
	}
	a.auditAs(r, ep.OrgID, su.user.Email, "endpoint.login", ep.ID, string(su.authMethod))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target+identity.EndpointCallbackPath+"?"+url.Values{"code": {code}}.Encode(), http.StatusFound)
}

func browserLoginEndpoint(ep *store.Endpoint) bool {
	if ep.Kind != store.KindHTTP || ep.Policy == nil || ep.Policy.Identity == nil {
		return false
	}
	return ep.Policy.Identity.BrowserLogin
}
