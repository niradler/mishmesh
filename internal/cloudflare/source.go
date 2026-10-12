package cloudflare

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

type Hostname struct {
	Host       string
	EndpointID string
	OrgID      string
}

type Source interface {
	DesiredHostnames(ctx context.Context) ([]Hostname, error)
}

type StoreSource struct {
	Data store.DataStore
	Orgs []string
}

func (s StoreSource) DesiredHostnames(ctx context.Context) ([]Hostname, error) {
	var out []Hostname
	for _, org := range s.Orgs {
		endpoints, err := s.Data.ListEndpointsByOrg(ctx, org)
		if err != nil {
			return nil, fmt.Errorf("list endpoints for org %s: %w", org, err)
		}
		for _, ep := range endpoints {
			if ep == nil || ep.Policy == nil || ep.Policy.FrontDoor != store.FrontDoorCloudflare {
				continue
			}
			host := normalizeHost(ep.Domain)
			if host == "" {
				continue
			}
			out = append(out, Hostname{Host: host, EndpointID: ep.ID, OrgID: ep.OrgID})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}
