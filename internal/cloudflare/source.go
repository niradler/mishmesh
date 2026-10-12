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
	Public     bool
}

type Source interface {
	Hostnames(ctx context.Context, orgID string) ([]Hostname, error)
}

type EndpointLister interface {
	ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error)
}

type StoreSource struct {
	Data EndpointLister
}

func (s StoreSource) Hostnames(ctx context.Context, orgID string) ([]Hostname, error) {
	endpoints, err := s.Data.ListEndpointsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list endpoints for org %s: %w", orgID, err)
	}
	var out []Hostname
	for _, ep := range endpoints {
		if ep == nil {
			continue
		}
		host := normalizeHost(ep.Domain)
		if host == "" {
			continue
		}
		public := ep.Policy != nil && ep.Policy.PublicAccess == store.PublicAccessCloudflare
		out = append(out, Hostname{Host: host, EndpointID: ep.ID, OrgID: ep.OrgID, Public: public})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, nil
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}
