package controlplane

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/store"
)

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}

type endpointDTO struct {
	ID        string                `json:"id"`
	AgentID   string                `json:"agent_id"`
	OrgID     string                `json:"org_id"`
	Kind      string                `json:"kind"`
	Method    string                `json:"method"`
	Lifecycle string                `json:"lifecycle"`
	Subdomain string                `json:"subdomain"`
	Domain    string                `json:"domain"`
	Port      int                   `json:"port"`
	PublicURL string                `json:"public_url"`
	Online    bool                  `json:"online"`
	Policy    *store.EndpointPolicy `json:"policy,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
}

type endpointInput struct {
	AgentID   string       `json:"agent_id"`
	Kind      string       `json:"kind"`
	Method    string       `json:"method"`
	Subdomain string       `json:"subdomain"`
	Domain    string       `json:"domain"`
	Port      int          `json:"port"`
	Policy    *policyInput `json:"policy"`
}

type policyInput struct {
	store.EndpointPolicy
	BasicAuthPassword string `json:"basic_auth_password,omitempty"`
}

func (a *API) toEndpointDTO(ep *store.Endpoint) endpointDTO {
	online := false
	if a.conns != nil {
		_, online = a.conns.ResolveEndpoint(ep.ID)
	}
	return endpointDTO{
		ID: ep.ID, AgentID: ep.AgentID, OrgID: ep.OrgID, Kind: ep.Kind, Method: store.MethodOrDefault(ep.Method),
		Lifecycle: ep.Lifecycle, Subdomain: ep.Subdomain, Domain: ep.Domain, Port: ep.Port,
		PublicURL: a.publicURL(ep), Online: online, Policy: redactPolicy(ep.Policy), CreatedAt: ep.CreatedAt,
	}
}

func (a *API) publicURL(ep *store.Endpoint) string {
	scheme := a.publicScheme
	if scheme == "" {
		scheme = "http"
	}
	switch {
	case ep.Domain != "":
		return fmt.Sprintf("%s://%s", scheme, ep.Domain)
	case ep.Kind == store.KindTCP && ep.Port > 0:
		return fmt.Sprintf("tcp://%s:%d", hostOnly(a.baseDomain), ep.Port)
	case ep.Subdomain != "":
		return fmt.Sprintf("%s://%s.%s", scheme, ep.Subdomain, a.baseDomain)
	default:
		return fmt.Sprintf("%s://%s/tunnel/%s", scheme, a.baseDomain, ep.ID)
	}
}

func redactPolicy(p *store.EndpointPolicy) *store.EndpointPolicy {
	if p == nil {
		return nil
	}
	clone := *p
	if clone.BasicAuthHash != "" {
		clone.BasicAuthHash = "set"
	}
	if clone.OIDC != nil {
		oc := *clone.OIDC
		if oc.ClientSecret != "" {
			oc.ClientSecret = "set"
		}
		clone.OIDC = &oc
	}
	return &clone
}

func (a *API) listOrgEndpointsHandler(w http.ResponseWriter, r *http.Request) {
	eps, err := a.data.ListEndpointsByOrg(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	out := make([]endpointDTO, 0, len(eps))
	for _, ep := range eps {
		out = append(out, a.toEndpointDTO(ep))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getEndpointHandler(w http.ResponseWriter, r *http.Request) {
	ep, err := a.data.GetEndpoint(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	if ep.OrgID != a.orgScope(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, a.toEndpointDTO(ep))
}

func (a *API) createEndpointHandler(w http.ResponseWriter, r *http.Request) {
	var req endpointInput
	if !decodeJSON(w, r, &req) {
		return
	}
	orgID := a.orgScope(r)
	kind := req.Kind
	if kind == "" {
		kind = store.KindHTTP
	}
	method := store.MethodOrDefault(req.Method)
	if !validMethod(method) {
		writeError(w, http.StatusBadRequest, "unknown method")
		return
	}
	pol, err := buildPolicy(req.Policy, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := req.AgentID
	if method == store.MethodProxy {
		if pol == nil || pol.ProxyTarget == "" {
			writeError(w, http.StatusBadRequest, "proxy_target required for proxy method")
			return
		}
		agentID = proxy.AgentID
	} else {
		ag, err := a.data.GetAgent(r.Context(), req.AgentID)
		if a.handleErr(w, err) {
			return
		}
		if ag.OrgID != orgID {
			writeError(w, http.StatusForbidden, "agent not in org")
			return
		}
		agentID = ag.ID
	}
	if q, err := a.data.GetQuota(r.Context(), orgID); err == nil && q.MaxEndpoints > 0 {
		if n, _ := a.data.CountEndpoints(r.Context(), orgID); n >= q.MaxEndpoints {
			writeError(w, http.StatusConflict, "endpoint quota exceeded")
			return
		}
	}
	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: agentID, OrgID: orgID, Kind: kind, Method: method,
		Lifecycle: store.LifecycleReserved, Subdomain: req.Subdomain, Domain: req.Domain,
		Port: req.Port, Policy: pol, CreatedAt: time.Now(),
	}
	if a.handleErr(w, a.data.CreateEndpoint(r.Context(), ep)) {
		return
	}
	if method == store.MethodProxy && a.conns != nil {
		a.conns.BindEndpoint(ep.ID, proxy.AgentID)
	}
	a.audit(r, "endpoint.create", ep.ID, kind)
	writeJSON(w, http.StatusCreated, a.toEndpointDTO(ep))
}

func (a *API) patchEndpointHandler(w http.ResponseWriter, r *http.Request) {
	ep, err := a.data.GetEndpoint(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	if ep.OrgID != a.orgScope(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Subdomain *string      `json:"subdomain"`
		Domain    *string      `json:"domain"`
		Port      *int         `json:"port"`
		Policy    *policyInput `json:"policy"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Subdomain != nil {
		ep.Subdomain = *req.Subdomain
	}
	if req.Domain != nil {
		ep.Domain = *req.Domain
	}
	if req.Port != nil {
		ep.Port = *req.Port
	}
	if req.Policy != nil {
		pol, err := buildPolicy(req.Policy, ep.Policy)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ep.Policy = pol
	}
	if a.handleErr(w, a.data.UpdateEndpoint(r.Context(), ep)) {
		return
	}
	a.audit(r, "endpoint.update", ep.ID, "")
	writeJSON(w, http.StatusOK, a.toEndpointDTO(ep))
}

func (a *API) deleteEndpointHandler(w http.ResponseWriter, r *http.Request) {
	ep, err := a.data.GetEndpoint(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	if ep.OrgID != a.orgScope(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if a.handleErr(w, a.data.DeleteEndpoint(r.Context(), ep.ID)) {
		return
	}
	if a.conns != nil {
		a.conns.UnbindEndpoint(ep.ID)
	}
	a.audit(r, "endpoint.delete", ep.ID, "")
	w.WriteHeader(http.StatusNoContent)
}

func validMethod(m string) bool {
	switch m {
	case store.MethodNative, store.MethodSSH, store.MethodProxy, store.MethodTailscale, store.MethodCloudflare:
		return true
	default:
		return false
	}
}

func buildPolicy(in *policyInput, existing *store.EndpointPolicy) (*store.EndpointPolicy, error) {
	if in == nil {
		return existing, nil
	}
	p := in.EndpointPolicy
	switch {
	case in.BasicAuthPassword != "":
		h, err := bcrypt.GenerateFromPassword([]byte(in.BasicAuthPassword), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash basic auth password: %w", err)
		}
		p.BasicAuthHash = string(h)
	case existing != nil:
		p.BasicAuthHash = existing.BasicAuthHash
	}
	if p.OIDC != nil && p.OIDC.ClientSecret == "set" && existing != nil && existing.OIDC != nil {
		p.OIDC.ClientSecret = existing.OIDC.ClientSecret
	}
	return &p, nil
}
