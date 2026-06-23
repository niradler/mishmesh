package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	AgentID     = "ag_proxy"
	SystemOrgID = "org_system"
)

type conn struct {
	data store.DataStore
	log  *slog.Logger
}

var _ store.AgentConn = (*conn)(nil)

func newConn(data store.DataStore, log *slog.Logger) *conn {
	return &conn{data: data, log: log}
}

func (c *conn) AgentID() string { return AgentID }

func (c *conn) OpenStream(ctx context.Context, endpointID, _ string, _ map[string]string) (net.Conn, error) {
	ep, err := c.data.GetEndpoint(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	if ep.Policy == nil || ep.Policy.ProxyTarget == "" {
		return nil, fmt.Errorf("proxy: endpoint %s has no proxy_target", endpointID)
	}
	if err := guardTarget(ep.Policy.ProxyTarget); err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "tcp", ep.Policy.ProxyTarget)
}

func (c *conn) Close() error { return nil }

func Register(ctx context.Context, data store.DataStore, conns store.ConnectionStore, log *slog.Logger) {
	ensureProxyAgent(ctx, data)
	conns.AddAgent(newConn(data, log))
	orgs, err := data.ListOrgs(ctx)
	if err != nil {
		return
	}
	for _, org := range orgs {
		eps, err := data.ListEndpointsByOrg(ctx, org.ID)
		if err != nil {
			continue
		}
		for _, ep := range eps {
			if ep.Method == store.MethodProxy {
				conns.BindEndpoint(ep.ID, AgentID)
			}
		}
	}
}

func ensureProxyAgent(ctx context.Context, data store.DataStore) {
	if _, err := data.GetOrg(ctx, SystemOrgID); err != nil {
		_ = data.CreateOrg(ctx, &store.Org{ID: SystemOrgID, Name: "system", CreatedAt: time.Now()})
	}
	if _, err := data.GetAgent(ctx, AgentID); err != nil {
		_ = data.CreateAgent(ctx, &store.Agent{ID: AgentID, OrgID: SystemOrgID, Name: "agentless-proxy", Status: store.AgentActive, CreatedAt: time.Now()})
	}
}

func guardTarget(target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("proxy: invalid target %q: %w", target, err)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("proxy: resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if isBlocked(ip) {
			return fmt.Errorf("proxy: target %q resolves to a blocked address", target)
		}
	}
	return nil
}

var metadataIP = net.IPv4(169, 254, 169, 254)

func isBlocked(ip net.IP) bool {
	if ip.Equal(metadataIP) {
		return true
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
