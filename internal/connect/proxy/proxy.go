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
	data  store.DataStore
	log   *slog.Logger
	guard *Guard
}

var _ store.AgentConn = (*conn)(nil)

func newConn(data store.DataStore, log *slog.Logger, guard *Guard) *conn {
	if guard == nil {
		guard = DefaultGuard()
	}
	return &conn{data: data, log: log, guard: guard}
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
	dialAddr, err := c.guard.Resolve(ctx, ep.Policy.ProxyTarget)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "tcp", dialAddr)
}

func (c *conn) Close() error { return nil }

func Register(ctx context.Context, data store.DataStore, conns store.ConnectionStore, log *slog.Logger, guard *Guard) {
	ensureProxyAgent(ctx, data)
	conns.AddAgent(newConn(data, log, guard))
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

func (c *conn) ClusterLocal() bool { return true }
