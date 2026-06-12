package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const defaultOrgID = "org_default"

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
	org = &store.Org{ID: defaultOrgID, Name: "default", CreatedAt: time.Now()}
	if err := a.data.CreateOrg(ctx, org); err != nil {
		return nil, err
	}
	return org, nil
}

func (a *API) createOrg(ctx context.Context, name string) (*store.Org, error) {
	org := &store.Org{ID: store.NewID("org"), Name: name, CreatedAt: time.Now()}
	if err := a.data.CreateOrg(ctx, org); err != nil {
		return nil, err
	}
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
	agent := &store.Agent{
		ID:        store.NewID("ag"),
		OrgID:     org.ID,
		Name:      name,
		Status:    store.AgentActive,
		CreatedAt: time.Now(),
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
		CreatedAt: time.Now(),
	}
	if err := a.data.CreateToken(ctx, tok); err != nil {
		return "", err
	}
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
	if a.conns != nil {
		if conn, ok := a.conns.GetAgent(agentID); ok {
			_ = conn.Close()
		}
	}
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
	if name != "" {
		agent.Name = name
	}
	if status != "" {
		switch status {
		case store.AgentActive, store.AgentDisabled, store.AgentRevoked:
			agent.Status = status
		default:
			return nil, fmt.Errorf("invalid status %q", status)
		}
	}
	if err := a.data.UpdateAgent(ctx, agent); err != nil {
		return nil, err
	}
	return agent, nil
}
