package connect

import (
	"context"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	AgentClientIP   = "0.0.0.0"
	DecisionTimeout = 5 * time.Second
)

type Meter interface {
	AddBytes(kind string, in, out int64)
}

type AgentDecision struct {
	scope     identity.Scope
	principal authz.EntityRef
	context   authz.RequestContext
}

func NewAgentDecision(agent *store.Agent, p identity.Principal, hasPrincipal bool, reqCtx authz.RequestContext) AgentDecision {
	d := AgentDecision{
		scope:     identity.Scope{OrgID: agent.OrgID, AgentID: agent.ID},
		principal: authz.EntityRef{Type: authz.EntityAgent, ID: agent.ID},
		context:   reqCtx,
	}
	d.context.ClientIP = AgentClientIP
	d.context.AuthMethod = authz.AuthMethodAgentToken
	if hasPrincipal {
		d.principal = p.Ref
		d.scope.TokenSessionID = p.SessionID
		d.context.AuthMethod = p.AuthMethod
		authTime := p.AuthTime.UTC()
		d.context.AuthTime = &authTime
	}
	return d
}

func (a AgentDecision) Decide(ctx context.Context, deciders identity.Deciders, action authz.Action, resource authz.EntityRef) (authz.Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, DecisionTimeout)
	defer cancel()
	return deciders.Decider(a.scope).Decide(ctx, authz.DecisionRequest{
		Principal: a.principal,
		Action:    action,
		Resource:  resource,
		Context:   a.context,
	})
}
