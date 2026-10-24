package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/endpointurl"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/subdomain"
)

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}

type endpointDTO struct {
	ID        string                 `json:"id"`
	AgentID   string                 `json:"agent_id"`
	OrgID     string                 `json:"org_id"`
	Kind      string                 `json:"kind"`
	Method    string                 `json:"method"`
	Lifecycle string                 `json:"lifecycle"`
	Subdomain string                 `json:"subdomain"`
	Domain    string                 `json:"domain"`
	Port      int                    `json:"port"`
	PublicURL string                 `json:"public_url"`
	Online    bool                   `json:"online"`
	Policy    *store.EndpointPolicy  `json:"policy,omitempty"`
	Health    *health.EndpointHealth `json:"health,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
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
	connectPresent    bool
}

func (input *policyInput) UnmarshalJSON(raw []byte) error {
	type plain policyInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*plain)(input)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	_, input.connectPresent = fields["connect"]
	return nil
}

func (a *API) toEndpointDTO(ep *store.Endpoint) endpointDTO {
	online := false
	if a.conns != nil {
		_, online = a.conns.ResolveEndpoint(ep.ID)
	}
	return endpointDTO{
		ID: ep.ID, AgentID: ep.AgentID, OrgID: ep.OrgID, Kind: ep.Kind, Method: store.MethodOrDefault(ep.Method),
		Lifecycle: ep.Lifecycle, Subdomain: ep.Subdomain, Domain: ep.Domain, Port: ep.Port,
		PublicURL: a.publicURL(ep), Online: online, Policy: redactPolicy(ep.Policy), Health: a.healthOf(ep.ID),
		CreatedAt: ep.CreatedAt,
	}
}

func (a *API) healthOf(endpointID string) *health.EndpointHealth {
	if a.endpointHealth == nil {
		return nil
	}
	h, ok := a.endpointHealth.Get(endpointID)
	if !ok {
		return nil
	}
	return &h
}

func (a *API) publicURL(ep *store.Endpoint) string {
	scheme := a.publicScheme
	if scheme == "" {
		scheme = "http"
	}
	switch {
	case ep.Kind == store.KindTLS:
		return endpointurl.TLS(ep, a.baseDomain, a.tlsPublicPort)
	case ep.Domain != "":
		return fmt.Sprintf("%s://%s", scheme, ep.Domain)
	case store.PortKind(ep.Kind):
		if ep.Port == 0 {
			return ""
		}
		return fmt.Sprintf("%s://%s:%d", ep.Kind, hostOnly(a.baseDomain), ep.Port)
	case ep.Subdomain != "":
		return fmt.Sprintf("%s://%s.%s", scheme, ep.Subdomain, a.baseDomain)
	default:
		if a.disablePathRouting {
			return ""
		}
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
	if msg := methodError(method); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	pol, err := buildPolicy(req.Policy, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.validatePolicyForMethod(method, pol); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Subdomain != "" {
		req.Subdomain = subdomain.Normalize(req.Subdomain)
		if err := subdomain.Validate(req.Subdomain, a.baseDomain); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	domain := req.Domain
	if domain != "" {
		var status int
		var msg string
		if domain, status, msg = a.authorizeDomainBinding(r.Context(), orgID, domain); status != 0 {
			writeError(w, status, msg)
			return
		}
	}
	connectionSnapshots := []*store.Connection{}
	if err := a.validateNetworkPolicy(r.Context(), orgID, "", domain, kind, pol, nil, &connectionSnapshots); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := req.AgentID
	if method == store.MethodProxy {
		agentID = proxy.AgentID
	} else {
		ag, err := a.data.GetAgent(r.Context(), req.AgentID)
		if a.handleErr(w, err) {
			return
		}
		if ag.OrgID != orgID {
			writeError(w, http.StatusNotFound, "not found")
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
		Lifecycle: store.LifecycleReserved, Subdomain: req.Subdomain, Domain: domain,
		Port: req.Port, Policy: pol, CreatedAt: time.Now(),
		ConnectionSnapshots: connectionSnapshots,
	}
	if a.endpointWriteError(w, a.data.CreateEndpoint(r.Context(), ep)) {
		return
	}
	if method == store.MethodProxy && a.conns != nil {
		a.conns.BindEndpoint(ep.ID, proxy.AgentID)
	}
	a.audit(r, "endpoint.create", ep.ID, kind)
	a.endpointChanged(ep)
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
	previousAccess := a.previousAccess(r.Context(), ep)
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
		sub := subdomain.Normalize(*req.Subdomain)
		if sub != "" {
			if err := subdomain.Validate(sub, a.baseDomain); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		ep.Subdomain = sub
	}
	if req.Domain != nil {
		domain := ""
		if *req.Domain != "" {
			var status int
			var msg string
			if domain, status, msg = a.authorizeDomainBinding(r.Context(), ep.OrgID, *req.Domain); status != 0 {
				writeError(w, status, msg)
				return
			}
		}
		ep.Domain = domain
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
		if err := a.validatePolicyForMethod(ep.Method, pol); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ep.Policy = pol
	}
	connectionSnapshots := []*store.Connection{}
	if err := a.validateNetworkPolicy(r.Context(), ep.OrgID, ep.ID, ep.Domain, ep.Kind, ep.Policy, previousAccess, &connectionSnapshots); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ep.ConnectionSnapshots = connectionSnapshots
	if a.endpointWriteError(w, a.data.UpdateEndpoint(r.Context(), ep)) {
		return
	}
	a.audit(r, "endpoint.update", ep.ID, "")
	a.endpointChanged(ep)
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
	a.endpointRemoved(ep)
	w.WriteHeader(http.StatusNoContent)
}

func methodError(m string) string {
	switch m {
	case store.MethodNative, store.MethodSSH, store.MethodProxy:
		return ""
	case store.MethodTailscale, store.MethodCloudflare:
		return "method " + m + " is not implemented"
	default:
		return "unknown method"
	}
}

func (a *API) endpointWriteError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "endpoint access or hostname changed or is already claimed; reload and retry")
		return true
	}
	return a.handleErr(w, err)
}

func (a *API) validatePolicyForMethod(method string, pol *store.EndpointPolicy) error {
	if method != store.MethodProxy {
		if pol != nil && len(pol.ProxyTargets) > 0 {
			return errors.New("proxy_targets is only valid for proxy method")
		}
		return nil
	}
	return a.validateProxyPolicy(pol)
}

func (a *API) validateProxyPolicy(pol *store.EndpointPolicy) error {
	if pol == nil || len(pol.ProxyTargets) == 0 {
		return errors.New("proxy_targets required for proxy method")
	}
	for _, target := range pol.ProxyTargets {
		if err := a.guardProxyTarget(target); err != nil {
			return err
		}
	}
	switch pol.LoadBalance {
	case "", store.LoadBalanceRoundRobin, store.LoadBalanceFailover:
	default:
		return fmt.Errorf("load_balance must be %s or %s", store.LoadBalanceRoundRobin, store.LoadBalanceFailover)
	}
	return proxy.ValidateOriginTLS(pol.OriginTLS)
}

func buildPolicy(in *policyInput, existing *store.EndpointPolicy) (*store.EndpointPolicy, error) {
	if in == nil {
		return existing, nil
	}
	p := in.EndpointPolicy
	if existing != nil && !in.connectPresent && p.Connect == nil {
		p.Connect = existing.Connect
	}
	if p.RateLimit != nil {
		if err := p.RateLimit.Validate(); err != nil {
			return nil, err
		}
	}
	if err := p.Identity.Validate(); err != nil {
		return nil, err
	}
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
