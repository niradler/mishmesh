package store

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/decryptscope"
)

const (
	MaxEgressCredentialAgents       = 64
	MaxEgressCredentialDestinations = 32
)

type EgressCredential struct {
	OrgID        string
	Name         string
	Header       string
	Prefix       string
	Secret       string
	AgentIDs     []string
	Destinations []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
	if len(c.Destinations) == 0 || len(c.Destinations) > MaxEgressCredentialDestinations {
		return fmt.Errorf("destinations needs 1 to %d host, *.domain or IP entries", MaxEgressCredentialDestinations)
	}
	normalized := make([]string, 0, len(c.Destinations))
	for _, raw := range c.Destinations {
		entry, err := normalizeCredentialDestination(raw)
		if err != nil {
			return fmt.Errorf("destinations: %w", err)
		}
		if !slices.Contains(normalized, entry) {
			normalized = append(normalized, entry)
		}
	}
	slices.Sort(normalized)
	c.Destinations = normalized
	return nil
}

func normalizeCredentialDestination(raw string) (string, error) {
	if addr, ok := credentialIP(raw); ok {
		return addr.String(), nil
	}
	patterns, err := decryptscope.Normalize([]string{raw})
	if err != nil {
		return "", err
	}
	return patterns[0], nil
}

func credentialIP(host string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (c *EgressCredential) ReleasableTo(host string) bool {
	if addr, ok := credentialIP(host); ok {
		return slices.Contains(c.Destinations, addr.String())
	}
	return decryptscope.Matches(c.Destinations, host)
}

func (c *EgressCredential) DeliversTo(agentID string) bool {
	return agentID != "" && slices.Contains(c.AgentIDs, agentID)
}
