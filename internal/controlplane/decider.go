package controlplane

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

type DecisionScope struct {
	OrgID         string
	AgentID       string
	SessionIDHash string
}

type serverDecider struct {
	api   *API
	scope DecisionScope
}

func (a *API) Decider(scope DecisionScope) authz.Decider {
	return serverDecider{api: a, scope: scope}
}

func (d serverDecider) Decide(ctx context.Context, req authz.DecisionRequest) (authz.Decision, error) {
	unavailable := func(err error) (authz.Decision, error) {
		return authz.Decision{Reason: authz.ReasonUnavailable, Owner: d.scope.OrgID}, err
	}
	az, err := d.api.orgAuthorizer(ctx, d.scope.OrgID)
	if err != nil {
		return unavailable(err)
	}
	if req.Validate() != nil {
		return az.Decide(req, authz.Facts{}), nil
	}
	facts, err := d.facts(ctx, req)
	if err != nil {
		return unavailable(err)
	}
	return az.Decide(req, facts), nil
}

func (d serverDecider) facts(ctx context.Context, req authz.DecisionRequest) (authz.Facts, error) {
	facts := authz.Facts{Now: d.api.now().UTC()}
	var err error
	if facts.Agent, err = d.actingAgent(ctx); err != nil {
		return facts, err
	}
	if facts.Subject, err = d.subject(ctx, req.Principal); err != nil {
		return facts, err
	}
	if req.Resource.Type == authz.EntityEndpoint {
		if facts.Endpoint, err = d.endpoint(ctx, req.Resource.ID, facts.Agent); err != nil {
			return facts, err
		}
	}
	if d.scope.SessionIDHash != "" {
		if facts.Session, err = d.session(ctx, req.Principal); err != nil {
			return facts, err
		}
	}
	return facts, nil
}

func (d serverDecider) loadAgent(ctx context.Context, id string) (*store.Agent, error) {
	ag, err := d.api.data.GetAgent(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load agent: %w", err)
	}
	if ag.OrgID != d.scope.OrgID {
		return nil, nil
	}
	return ag, nil
}

func (d serverDecider) actingAgent(ctx context.Context) (*authz.AgentEntity, error) {
	if d.scope.AgentID == "" {
		return nil, nil
	}
	ag, err := d.loadAgent(ctx, d.scope.AgentID)
	if ag == nil || err != nil {
		return nil, err
	}
	return &authz.AgentEntity{ID: ag.ID, Name: ag.Name, Disabled: ag.Status != store.AgentActive}, nil
}

func (d serverDecider) subject(ctx context.Context, ref authz.EntityRef) (*authz.Subject, error) {
	switch ref.Type {
	case authz.EntityAnonymous:
		return &authz.Subject{Ref: ref}, nil
	case authz.EntityAgent:
		ag, err := d.loadAgent(ctx, ref.ID)
		if ag == nil || err != nil {
			return nil, err
		}
		return &authz.Subject{Ref: ref, Name: ag.Name, Disabled: ag.Status != store.AgentActive}, nil
	case authz.EntityUser:
		return d.userSubject(ctx, ref)
	}
	return nil, nil
}

func (d serverDecider) userSubject(ctx context.Context, ref authz.EntityRef) (*authz.Subject, error) {
	m, err := d.api.data.GetMembership(ctx, d.scope.OrgID, ref.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load membership: %w", err)
	}
	u, err := d.api.data.GetUserByID(ctx, ref.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	groups, err := d.api.data.ListMemberGroups(ctx, d.scope.OrgID, store.GroupMemberUser, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("load groups: %w", err)
	}
	subject := &authz.Subject{Ref: ref, Email: u.Email, Name: u.Name, Role: m.Role}
	for _, g := range groups {
		subject.Groups = append(subject.Groups, authz.GroupRef{ID: g.ID, Name: g.Name})
	}
	return subject, nil
}

func (d serverDecider) endpoint(ctx context.Context, id string, acting *authz.AgentEntity) (*authz.EndpointEntity, error) {
	ep, err := d.api.data.GetEndpoint(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load endpoint: %w", err)
	}
	if ep.OrgID != d.scope.OrgID {
		return nil, nil
	}
	entity := &authz.EndpointEntity{ID: ep.ID, Kind: ep.Kind, Method: ep.Method}
	if ep.Policy == nil || ep.Policy.Connect == nil {
		return entity, nil
	}
	for _, allowed := range ep.Policy.Connect.AllowAgents {
		if allowed == store.ConnectAnyAgent {
			entity.ConnectAnyAgent = true
			continue
		}
		entity.ConnectAgents = append(entity.ConnectAgents, allowed)
	}
	if acting != nil && acting.Name != "" && slices.Contains(entity.ConnectAgents, acting.Name) {
		entity.ConnectAgents = append(entity.ConnectAgents, acting.ID)
	}
	return entity, nil
}

func (d serverDecider) session(ctx context.Context, principal authz.EntityRef) (*authz.SessionFacts, error) {
	s, err := d.api.data.GetSession(ctx, d.scope.SessionIDHash)
	if errors.Is(err, store.ErrNotFound) {
		return &authz.SessionFacts{Revoked: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	owned := principal.Type == authz.EntityUser && s.UserID == principal.ID && s.OrgID == d.scope.OrgID
	return &authz.SessionFacts{ExpiresAt: s.ExpiresAt, Revoked: !owned}, nil
}
