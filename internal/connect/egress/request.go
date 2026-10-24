package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/credseal"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	ReasonCredentialUnavailable = "credential_unavailable"
	ReasonCredentialRequiresTLS = "credential_requires_tls"
	ReasonCredentialDestination = "credential_destination_refused"
	ReasonCredentialMethod      = "credential_method_refused"
)

type Credentials interface {
	GetEgressCredential(ctx context.Context, orgID, name string) (*store.EgressCredential, error)
}

type Secrets interface {
	Open(ctx context.Context, orgID, name string) ([]byte, error)
}

func (h *Handler) DecideRequest(ctx context.Context, agent *store.Agent, q tunnel.DecisionQuery) tunnel.DecisionAnswer {
	if q.Action != tunnel.DecisionActionEgressRequest || q.HTTP == nil {
		return tunnel.DecisionAnswer{Reason: string(authz.ReasonInvalidRequest)}
	}
	hq := q.HTTP
	if hq.Port < 1 || hq.Port > 65535 {
		return tunnel.DecisionAnswer{Reason: string(authz.ReasonInvalidRequest)}
	}
	dest, err := authz.NormalizeDestination(net.JoinHostPort(hq.Host, strconv.Itoa(hq.Port)))
	if err != nil {
		return tunnel.DecisionAnswer{Reason: string(authz.ReasonInvalidRequest)}
	}
	p, hasPrincipal, refused := h.principal(ctx, agent, hq.Auth)
	if refused != nil {
		return tunnel.DecisionAnswer{Reason: refused.reason}
	}
	exit, refused := h.resolveExit(ctx, agent, hq.Exit)
	if refused != nil {
		return tunnel.DecisionAnswer{Reason: refused.reason}
	}
	d, ok := h.decideRequest(ctx, agent, p, hasPrincipal, dest, hq, exit)
	if !ok {
		return tunnel.DecisionAnswer{Reason: string(d.Reason)}
	}
	answer := tunnel.DecisionAnswer{
		Allowed:    true,
		Reason:     string(d.Reason),
		TTLSeconds: d.TTLSeconds,
		Scope:      &tunnel.DecisionScope{Path: d.Scope.Path, Method: d.Scope.Method},
	}
	name, err := authz.SelectedCredential(d)
	if err != nil {
		return tunnel.DecisionAnswer{Reason: string(authz.ReasonPolicyError)}
	}
	if name == "" {
		return answer
	}
	if !hq.TLS {
		return tunnel.DecisionAnswer{Reason: ReasonCredentialRequiresTLS}
	}
	if hq.Method == http.MethodTrace {
		return tunnel.DecisionAnswer{Reason: ReasonCredentialMethod}
	}
	cred, reason := h.releaseCredential(ctx, agent, name, hq.Host, q.SealTo)
	if cred == nil {
		return tunnel.DecisionAnswer{Reason: reason}
	}
	answer.Credential = cred
	return answer
}

func (h *Handler) decideRequest(ctx context.Context, agent *store.Agent, p identity.Principal, hasPrincipal bool, dest string, hq *tunnel.HTTPQuery, exit *store.Agent) (authz.Decision, bool) {
	if h.deciders == nil {
		return authz.Decision{Reason: authz.ReasonUnavailable}, false
	}
	scope := identity.Scope{OrgID: agent.OrgID, AgentID: agent.ID}
	principal := authz.EntityRef{Type: authz.EntityAgent, ID: agent.ID}
	reqCtx := authz.RequestContext{
		ClientIP:   agentClientIP,
		AuthMethod: authz.AuthMethodAgentToken,
		Method:     hq.Method,
		Path:       hq.Path,
		TLS:        hq.TLS,
		Decrypted:  hq.Decrypted,
	}
	if exit != nil {
		reqCtx.Exit = exit.ID
	}
	if hasPrincipal {
		principal = p.Ref
		scope.TokenSessionID = p.SessionID
		reqCtx.AuthMethod = p.AuthMethod
		authTime := p.AuthTime.UTC()
		reqCtx.AuthTime = &authTime
	}
	decideCtx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	d, err := h.deciders.Decider(scope).Decide(decideCtx, authz.DecisionRequest{
		Principal: principal,
		Action:    authz.ActionEgressRequest,
		Resource:  authz.EntityRef{Type: authz.EntityDestination, ID: dest},
		Context:   reqCtx,
	})
	switch {
	case err != nil || d.Reason == authz.ReasonUnavailable:
		return authz.Decision{Reason: authz.ReasonUnavailable}, false
	case !d.Allowed:
		if d.Reason == "" {
			d.Reason = authz.ReasonPolicyDeny
		}
		return d, false
	}
	return d, true
}

func (h *Handler) releaseCredential(ctx context.Context, agent *store.Agent, name, host string, sealTo []byte) (*tunnel.SealedCredential, string) {
	if h.credentials == nil || h.secrets == nil || len(sealTo) != credseal.PublicKeySize {
		return nil, ReasonCredentialUnavailable
	}
	cred, err := h.credentials.GetEgressCredential(ctx, agent.OrgID, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, ReasonCredentialUnavailable
	case err != nil:
		h.log.Error("egress credential lookup failed", "org_id", agent.OrgID, "credential", name, "err", err)
		return nil, string(authz.ReasonUnavailable)
	}
	if cred.OrgID != agent.OrgID || !cred.DeliversTo(agent.ID) {
		h.log.Warn("egress credential not scoped to agent", "org_id", agent.OrgID, "agent_id", agent.ID, "credential", name)
		return nil, ReasonCredentialUnavailable
	}
	if !cred.ReleasableTo(host) {
		h.log.Warn("egress credential refused for destination", "org_id", agent.OrgID, "agent_id", agent.ID, "credential", name, "host", host)
		return nil, ReasonCredentialDestination
	}
	secret, err := h.secrets.Open(ctx, agent.OrgID, cred.Secret)
	if err != nil {
		h.log.Warn("egress credential secret unavailable", "org_id", agent.OrgID, "credential", name, "err", err)
		return nil, ReasonCredentialUnavailable
	}
	value := append([]byte(cred.Prefix), secret...)
	clear(secret)
	sealed, err := credseal.Seal(sealTo, credseal.Binding{OrgID: agent.OrgID, AgentID: agent.ID, Name: name}, value)
	clear(value)
	if err != nil {
		h.log.Warn("egress credential seal failed", "agent_id", agent.ID, "credential", name, "err", err)
		return nil, ReasonCredentialUnavailable
	}
	return &tunnel.SealedCredential{Name: name, Header: cred.Header, Sealed: sealed}, ""
}
