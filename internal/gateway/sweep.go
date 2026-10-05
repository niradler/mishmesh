package gateway

import (
	"context"
	"fmt"

	"github.com/mishmesh/mishmesh/internal/store"
)

func (g *Gateway) SweepOrphanedEphemeral(ctx context.Context) (int, error) {
	orgs, err := g.data.ListOrgs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list orgs: %w", err)
	}
	swept := 0
	for _, org := range orgs {
		eps, err := g.data.ListEndpointsByOrg(ctx, org.ID)
		if err != nil {
			return swept, fmt.Errorf("list endpoints for org %s: %w", org.ID, err)
		}
		for _, ep := range eps {
			if ep.Lifecycle != store.LifecycleEphemeral || g.agentHasLiveSession(ep.AgentID) {
				continue
			}
			if err := g.data.DeleteEndpoint(ctx, ep.ID); err != nil {
				return swept, fmt.Errorf("delete endpoint %s: %w", ep.ID, err)
			}
			g.log.Info("swept orphaned ephemeral endpoint", "endpoint_id", ep.ID, "agent_id", ep.AgentID, "kind", ep.Kind)
			swept++
		}
	}
	return swept, nil
}

func (g *Gateway) agentHasLiveSession(agentID string) bool {
	if _, ok := g.conns.GetAgent(agentID); ok {
		return true
	}
	return g.conns.OwnedElsewhere(agentID)
}
