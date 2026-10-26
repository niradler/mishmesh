package controlplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	defaultOrgID     = "org_default"
	bootstrapAgentID = "ag_bootstrap"
)

func (a *API) EnsureBootstrap(ctx context.Context, rawToken string) (string, error) {
	org, err := a.ensureOrg(ctx, defaultOrgID)
	if err != nil {
		return "", err
	}
	hash := store.HashToken(rawToken)
	if tok, err := a.data.GetTokenByHash(ctx, hash); err == nil {
		return tok.AgentID, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if _, err := a.data.GetAgent(ctx, bootstrapAgentID); errors.Is(err, store.ErrNotFound) {
		agent := &store.Agent{ID: bootstrapAgentID, OrgID: org.ID, Name: "bootstrap", Status: store.AgentActive, CreatedAt: a.now()}
		if err := a.data.CreateAgent(ctx, agent); err != nil {
			existing, lookupErr := a.data.GetAgent(ctx, bootstrapAgentID)
			if lookupErr != nil || existing.OrgID != org.ID || existing.Status != store.AgentActive {
				return "", err
			}
		}
	} else if err != nil {
		return "", err
	}
	tok := &store.Token{ID: store.NewID("tok"), OrgID: org.ID, AgentID: bootstrapAgentID, Hash: hash, CreatedAt: a.now()}
	if err := a.data.CreateToken(ctx, tok); err != nil {
		existing, lookupErr := a.data.GetTokenByHash(ctx, hash)
		if lookupErr != nil || existing.AgentID != bootstrapAgentID || existing.OrgID != org.ID {
			return "", err
		}
	}
	return bootstrapAgentID, nil
}

var (
	errAgentNotRevoked = errors.New("agent must be revoked before deletion")
)

func (a *API) ensureOrg(ctx context.Context, orgID string) (*store.Org, error) {
	if orgID == "" {
		orgID = defaultOrgID
	}
	org, err := a.data.GetOrg(ctx, orgID)
	if err == nil {
		return org, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if orgID != defaultOrgID {
		return nil, store.ErrNotFound
	}
	org = &store.Org{ID: defaultOrgID, Name: "default", CreatedAt: a.now()}
	if err := a.data.CreateOrg(ctx, org); err != nil {
		if existing, getErr := a.data.GetOrg(ctx, defaultOrgID); getErr == nil {
			return existing, nil
		}
		return nil, err
	}
	a.applyDefaultQuota(ctx, org.ID)
	return org, nil
}

func (a *API) createOrg(ctx context.Context, name string) (*store.Org, error) {
	org := &store.Org{ID: store.NewID("org"), Name: name, CreatedAt: a.now()}
	if err := a.data.CreateOrg(ctx, org); err != nil {
		return nil, err
	}
	a.applyDefaultQuota(ctx, org.ID)
	return org, nil
}

func (a *API) createAgent(ctx context.Context, orgID, name string) (*store.Agent, string, error) {
	org, err := a.ensureOrg(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		name = "agent"
	}
	if err := a.enforceAgentQuota(ctx, org.ID); err != nil {
		return nil, "", err
	}
	agent := &store.Agent{
		ID:        store.NewID("ag"),
		OrgID:     org.ID,
		Name:      name,
		Status:    store.AgentActive,
		CreatedAt: a.now(),
	}
	if err := a.data.CreateAgent(ctx, agent); err != nil {
		return nil, "", err
	}
	raw, err := a.issueToken(ctx, agent)
	if err != nil {
		return nil, "", err
	}
	return agent, raw, nil
}

func (a *API) issueToken(ctx context.Context, agent *store.Agent) (string, error) {
	raw, hash, err := store.GenerateToken()
	if err != nil {
		return "", err
	}
	tok := &store.Token{
		ID:        store.NewID("tok"),
		OrgID:     agent.OrgID,
		AgentID:   agent.ID,
		Hash:      hash,
		CreatedAt: a.now(),
	}
	if err := a.data.CreateToken(ctx, tok); err != nil {
		return "", err
	}
	return raw, nil
}

func (a *API) rotateToken(ctx context.Context, agent *store.Agent) (string, error) {
	previous, err := a.data.ListTokensByAgent(ctx, agent.ID)
	if err != nil {
		return "", err
	}
	raw, err := a.issueToken(ctx, agent)
	if err != nil {
		return "", err
	}
	for _, t := range previous {
		if t.RevokedAt != nil {
			continue
		}
		if err := a.data.RevokeToken(ctx, t.ID); err != nil {
			return "", fmt.Errorf("revoke previous token: %w", err)
		}
	}
	a.closeAgentConn(agent.ID)
	return raw, nil
}

func (a *API) revokeAgent(ctx context.Context, agentID string) error {
	agent, err := a.data.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	agent.Status = store.AgentRevoked
	if err := a.data.UpdateAgent(ctx, agent); err != nil {
		return err
	}
	if err := a.data.RevokeTokensByAgent(ctx, agentID); err != nil {
		return err
	}
	a.closeAgentConn(agentID)
	return nil
}

func (a *API) deleteAgent(ctx context.Context, agentID string) error {
	agent, err := a.data.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if agent.Status != store.AgentRevoked {
		return errAgentNotRevoked
	}
	return a.data.DeleteAgent(ctx, agentID)
}

func (a *API) connected(agentID string) bool {
	if a.conns == nil {
		return false
	}
	_, ok := a.conns.GetAgent(agentID)
	return ok
}

func (a *API) updateAgent(ctx context.Context, agentID, name, status string) (*store.Agent, error) {
	agent, err := a.data.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	switch status {
	case "", store.AgentActive, store.AgentDisabled, store.AgentRevoked:
	default:
		return nil, fmt.Errorf("invalid status %q", status)
	}
	if name != "" {
		agent.Name = name
	}
	if status != "" && status != store.AgentRevoked {
		agent.Status = status
	}
	if err := a.data.UpdateAgent(ctx, agent); err != nil {
		return nil, err
	}
	switch status {
	case store.AgentRevoked:
		if err := a.revokeAgent(ctx, agentID); err != nil {
			return nil, err
		}
		agent.Status = store.AgentRevoked
	case store.AgentDisabled:
		a.closeAgentConn(agentID)
	}
	return agent, nil
}

func (a *API) closeAgentConn(agentID string) {
	if a.conns == nil {
		return
	}
	if conn, ok := a.conns.GetAgent(agentID); ok {
		_ = conn.Close()
	}
}
