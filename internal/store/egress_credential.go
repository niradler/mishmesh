package store

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

const MaxEgressCredentialAgents = 64

type EgressCredential struct {
	OrgID     string
	Name      string
	Header    string
	Prefix    string
	Secret    string
	AgentIDs  []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (c *EgressCredential) Validate() error {
	if !ValidCredentialName(c.Name) {
		return fmt.Errorf("name %q must match %s", c.Name, credentialNamePattern)
	}
	if err := (EndpointCredential{Header: c.Header, Secret: c.Secret, Prefix: c.Prefix}).validate("credential"); err != nil {
		return err
	}
	if len(c.AgentIDs) > MaxEgressCredentialAgents {
		return fmt.Errorf("agent_ids allows at most %d entries", MaxEgressCredentialAgents)
	}
	for _, id := range c.AgentIDs {
		if id == "" || len(id) > 128 {
			return errors.New("agent_ids entries must be agent ids")
		}
	}
	return nil
}

func (c *EgressCredential) DeliversTo(agentID string) bool {
	return agentID != "" && slices.Contains(c.AgentIDs, agentID)
}
