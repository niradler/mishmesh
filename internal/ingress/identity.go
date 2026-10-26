package ingress

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/clientip"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

const (
	identityAuthHeader   = "X-Mishmesh-Auth"
	mishmeshHeaderPrefix = "X-Mishmesh-"
	mishmeshCookiePrefix = "mm_"
	unknownClientIP      = "0.0.0.0"
)

type identityKey struct{}

type requestIdentity struct {
	principal  *identity.Principal
	decision   authz.Decision
	credential *upstreamCredential
}

func identityFrom(r *http.Request) *requestIdentity {
	id, _ := r.Context().Value(identityKey{}).(*requestIdentity)
	return id
}

func presentedToken(r *http.Request, idp *store.EndpointIdentity) string {
	if v := token.Bearer(r.Header.Get(identityAuthHeader)); v != "" {
		return v
	}
	if idp.AuthorizationHeader {
		if v := token.Bearer(r.Header.Get("Authorization")); token.LooksLikeToken(v) {
			return v
		}
	}
	return ""
}

func writeIdentityUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="mishmesh"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func writeIdentityUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "authorization temporarily unavailable", http.StatusServiceUnavailable)
}

func (i *Ingress) applyIdentityGate(w http.ResponseWriter, r *http.Request, ep *store.Endpoint, outPath string) (*http.Request, bool) {
	if ep.Policy == nil || ep.Policy.Identity == nil {
		return r, true
	}
	if i.identity == nil || i.deciders == nil {
		writeIdentityUnavailable(w)
		i.recordCode(http.StatusServiceUnavailable)
		return r, false
	}
	if !decisionPathMatchesUpstream(r, outPath) {
		http.Error(w, "bad request path", http.StatusBadRequest)
		i.recordCode(http.StatusBadRequest)
		return r, false
	}
	idp := ep.Policy.Identity
	var principal *identity.Principal
	raw := presentedToken(r, idp)
	if raw != "" {
		p, err := i.identity.Authenticate(r.Context(), raw, token.TypeAccess)
		switch {
		case errors.Is(err, identity.ErrUnavailable):
			i.log.Error("identity lookup failed", "endpoint_id", ep.ID, "err", err)
			writeIdentityUnavailable(w)
			i.recordCode(http.StatusServiceUnavailable)
			return r, false
		case err != nil:
			writeIdentityUnauthorized(w)
			i.recordCode(http.StatusUnauthorized)
			return r, false
		case p.OrgID != ep.OrgID:
			i.writeResolveError(w, errTunnelNotFound)
			return r, false
		}
		principal = &p
	} else if idp.BrowserLogin {
		p, err := i.endpointSessionPrincipal(r, ep)
		if err != nil {
			i.log.Error("endpoint session lookup failed", "endpoint_id", ep.ID, "err", err)
			writeIdentityUnavailable(w)
			i.recordCode(http.StatusServiceUnavailable)
			return r, false
		}
		principal = p
	}
	browser := raw == "" && i.canRedirectToLogin(r, idp)
	if principal == nil && idp.Required {
		if browser {
			i.redirectToLogin(w, r, ep, false)
			return r, false
		}
		writeIdentityUnauthorized(w)
		i.recordCode(http.StatusUnauthorized)
		return r, false
	}
	decision, err := i.decideEndpoint(r, ep, outPath, principal)
	accessFrom(r).setDecision(principal, decision)
	if err != nil || decision.Reason == authz.ReasonUnavailable {
		i.log.Error("endpoint decision unavailable", "endpoint_id", ep.ID, "err", err)
		writeIdentityUnavailable(w)
		i.recordCode(http.StatusServiceUnavailable)
		return r, false
	}
	if !decision.Allowed {
		switch {
		case browser && principal == nil:
			i.redirectToLogin(w, r, ep, false)
		case browser && decision.NeedsReauth() && !reauthWouldRepeat(principal):
			i.redirectToLogin(w, r, ep, true)
		case principal == nil:
			writeIdentityUnauthorized(w)
			i.recordCode(http.StatusUnauthorized)
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
			i.recordCode(http.StatusForbidden)
		}
		return r, false
	}
	cred, err := i.resolveCredential(r.Context(), ep, decision)
	if err != nil {
		accessFrom(r).setDecision(principal, authz.Decision{Reason: reasonCredentialUnavailable, Policies: decision.Policies})
		i.log.Error("credential injection failed, refusing request", "endpoint_id", ep.ID, "org_id", ep.OrgID, "err", err)
		writeCredentialFailure(w)
		i.recordCode(http.StatusBadGateway)
		return r, false
	}
	ctx := context.WithValue(r.Context(), identityKey{}, &requestIdentity{principal: principal, decision: decision, credential: cred})
	return r.WithContext(ctx), true
}

func reauthWouldRepeat(p *identity.Principal) bool {
	return !p.AuthTime.IsZero() && time.Since(p.AuthTime) <= identity.EndpointReauthMaxAge
}

func decisionPathMatchesUpstream(r *http.Request, outPath string) bool {
	if strings.ContainsAny(outPath, `\;`) {
		return false
	}
	return canonicalDecisionPath(outPath) == outPath
}

func canonicalDecisionPath(p string) string {
	clean := path.Clean("/" + p)
	if clean != "/" && strings.HasSuffix(p, "/") {
		return clean + "/"
	}
	return clean
}

func (i *Ingress) decideEndpoint(r *http.Request, ep *store.Endpoint, outPath string, principal *identity.Principal) (authz.Decision, error) {
	ref := authz.EntityRef{Type: authz.EntityAnonymous, ID: authz.AnonymousID}
	scope := identity.Scope{OrgID: ep.OrgID}
	reqCtx := authz.RequestContext{
		ClientIP:   unknownClientIP,
		Host:       hostOnly(r.Host),
		TLS:        requestIsHTTPS(r, i.trusted),
		Method:     r.Method,
		Path:       canonicalDecisionPath(outPath),
		AuthMethod: authz.AuthMethodNone,
	}
	if ip := clientip.Resolve(r, i.trusted); ip != nil {
		reqCtx.ClientIP = ip.String()
	}
	if principal != nil {
		ref = principal.Ref
		scope = principal.Scope("")
		reqCtx.AuthMethod = principal.AuthMethod
		authTime := principal.AuthTime.UTC()
		reqCtx.AuthTime = &authTime
	}
	decider := i.deciders.Decider(scope)
	var decision authz.Decision
	var policies []authz.DeterminingPolicy
	for _, action := range []authz.Action{authz.ActionEndpointAccess, authz.ActionHTTPRequest} {
		d, err := decider.Decide(r.Context(), authz.DecisionRequest{
			Principal: ref,
			Action:    action,
			Resource:  authz.EntityRef{Type: authz.EntityEndpoint, ID: ep.ID},
			Context:   reqCtx,
		})
		if err != nil || !d.Allowed {
			return d, err
		}
		policies = append(policies, d.Policies...)
		decision = d
	}
	decision.Policies = mergePolicies(policies)
	return decision, nil
}

func sanitizeUpstreamRequest(h http.Header, ep *store.Endpoint) {
	for name := range h {
		canonical := http.CanonicalHeaderKey(name)
		if strings.HasPrefix(canonical, mishmeshHeaderPrefix) || strings.HasPrefix(canonical, "X-Forwarded-") {
			h.Del(name)
		}
	}
	h.Del("X-Real-Ip")
	h.Del("Forwarded")
	stripCookiesMatching(h, func(name string) bool { return strings.HasPrefix(name, mishmeshCookiePrefix) })
	if ep != nil && ep.Policy != nil && ep.Policy.Identity != nil && ep.Policy.Identity.AuthorizationHeader {
		if token.LooksLikeToken(token.Bearer(h.Get("Authorization"))) {
			h.Del("Authorization")
		}
	}
}

func dropH2CUpgrade(h http.Header) {
	if strings.EqualFold(strings.TrimSpace(h.Get("Upgrade")), "h2c") {
		h.Del("Upgrade")
		h.Del("Http2-Settings")
	}
}

func sanitizeUpstreamResponse(h http.Header) {
	values := h.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	kept := values[:0:0]
	for _, v := range values {
		name, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(strings.TrimSpace(name), mishmeshCookiePrefix) {
			continue
		}
		kept = append(kept, v)
	}
	h.Del("Set-Cookie")
	for _, v := range kept {
		h.Add("Set-Cookie", v)
	}
}
