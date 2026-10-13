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
	Data         EndpointLister
	ConnectionID string
}

func AccessHosts(connectionID string, endpoints []*store.Endpoint) []Hostname {
	out := make([]Hostname, 0)
	for _, endpoint := range endpoints {
		if endpoint == nil || endpoint.Policy == nil || connectionID == "" {
			continue
		}
		for _, ref := range endpoint.Policy.Access {
			if ref.ConnectionID != connectionID {
				continue
			}
			host := ref.Hostname
			if host == "" {
				host = endpoint.Domain
			}
			if host = normalizeHost(host); host != "" {
				out = append(out, Hostname{Host: host, EndpointID: endpoint.ID, OrgID: endpoint.OrgID, Public: true})
			}
		}
	}
	return dedupeHostnames(out)
}

func (s StoreSource) Hostnames(ctx context.Context, orgID string) ([]Hostname, error) {
	endpoints, err := s.Data.ListEndpointsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list endpoints for org %s: %w", orgID, err)
	}
	if s.ConnectionID != "" {
		return s.connectionHosts(orgID, endpoints), nil
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

func (s StoreSource) connectionHosts(orgID string, endpoints []*store.Endpoint) []Hostname {
	owned := make([]*store.Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint != nil && endpoint.OrgID == orgID {
			owned = append(owned, endpoint)
		}
	}
	out := AccessHosts(s.ConnectionID, owned)
	selected := make(map[string]bool, len(out))
	for _, host := range out {
		selected[host.Host] = true
	}
	observed := make(map[string]bool)
	for _, endpoint := range owned {
		names := []string{endpoint.Domain}
		if endpoint.Policy != nil {
			for _, ref := range endpoint.Policy.Access {
				names = append(names, ref.Hostname)
			}
		}
		for _, name := range names {
			host := normalizeHost(name)
			if host != "" && !selected[host] && !observed[host] {
				out = append(out, Hostname{Host: host, EndpointID: endpoint.ID, OrgID: orgID})
				observed[host] = true
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}
