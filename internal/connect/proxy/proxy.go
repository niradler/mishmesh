package proxy

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	AgentID     = "ag_proxy"
	SystemOrgID = "org_system"

	dialTimeout = 10 * time.Second
	dialBudget  = 30 * time.Second
)

type TargetHealth interface {
	TargetHealthy(endpointID, target string) (healthy bool, known bool)
}

type Connector struct {
	data       store.DataStore
	log        *slog.Logger
	guard      *Guard
	counters   sync.Map
	mu         sync.RWMutex
	health     TargetHealth
	dialBudget time.Duration
}

var _ store.AgentConn = (*Connector)(nil)

func newConn(data store.DataStore, log *slog.Logger, guard *Guard) *Connector {
	if guard == nil {
		guard = DefaultGuard()
	}
	return &Connector{data: data, log: log, guard: guard, dialBudget: dialBudget}
}

func (c *Connector) AgentID() string { return AgentID }

func (c *Connector) SetTargetHealth(h TargetHealth) {
	c.mu.Lock()
	c.health = h
	c.mu.Unlock()
}

func (c *Connector) targetHealth() TargetHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

func (c *Connector) counter(endpointID string) *atomic.Uint64 {
	if v, ok := c.counters.Load(endpointID); ok {
		return v.(*atomic.Uint64)
	}
	v, _ := c.counters.LoadOrStore(endpointID, new(atomic.Uint64))
	return v.(*atomic.Uint64)
}

func (c *Connector) orderTargets(endpointID, clientIP string, pol *store.EndpointPolicy) []string {
	targets := pol.ProxyTargets
	if len(targets) < 2 {
		return targets
	}
	h := c.targetHealth()
	preferred := make([]string, 0, len(targets))
	fallback := make([]string, 0, len(targets))
	for _, t := range targets {
		if h != nil {
			if healthy, known := h.TargetHealthy(endpointID, t); known && !healthy {
				fallback = append(fallback, t)
				continue
			}
		}
		preferred = append(preferred, t)
	}
	switch {
	case len(preferred) < 2 || pol.LoadBalance == store.LoadBalanceFailover:
	case pol.LoadBalance == store.LoadBalanceSticky && clientIP != "":
		preferred = rendezvousOrder(clientIP, preferred)
	default:
		offset := int((c.counter(endpointID).Add(1) - 1) % uint64(len(preferred)))
		rotated := make([]string, 0, len(preferred))
		rotated = append(rotated, preferred[offset:]...)
		rotated = append(rotated, preferred[:offset]...)
		preferred = rotated
	}
	return append(preferred, fallback...)
}

func rendezvousScore(clientIP, target string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(clientIP))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write([]byte(target))
	return h.Sum64()
}

func rendezvousOrder(clientIP string, targets []string) []string {
	ordered := slices.Clone(targets)
	scores := make(map[string]uint64, len(ordered))
	for _, t := range ordered {
		scores[t] = rendezvousScore(clientIP, t)
	}
	slices.SortStableFunc(ordered, func(a, b string) int {
		return cmp.Compare(scores[b], scores[a])
	})
	return ordered
}

func targetMatchesKind(target, kind string) bool {
	t, err := ParseTarget(target)
	if err != nil {
		return false
	}
	return t.Datagram() == (kind == store.KindUDP)
}

func (c *Connector) OpenStream(ctx context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	ep, err := c.data.GetEndpoint(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	if ep.Policy == nil || len(ep.Policy.ProxyTargets) == 0 {
		return nil, fmt.Errorf("proxy: endpoint %s has no proxy_targets", endpointID)
	}
	var lastErr error
	ctx, cancel := context.WithTimeout(ctx, c.dialBudget)
	defer cancel()
	for _, target := range c.orderTargets(endpointID, meta[tunnel.MetaClientIP], ep.Policy) {
		if !targetMatchesKind(target, kind) {
			lastErr = fmt.Errorf("target %s does not carry %s traffic", target, kind)
			continue
		}
		conn, err := c.guard.Dial(ctx, target, ep.Policy.OriginTLS, dialTimeout)
		if err == nil {
			if kind == store.KindUDP {
				return datagramStream(conn, meta), nil
			}
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("proxy: endpoint %s: all targets failed: %w", endpointID, lastErr)
}

func datagramStream(socket net.Conn, meta map[string]string) net.Conn {
	near, far := net.Pipe()
	go tunnel.RelayDatagrams(far, socket, tunnel.DatagramIdleFromMeta(meta))
	return near
}

func (c *Connector) Close() error { return nil }

func Register(ctx context.Context, data store.DataStore, conns store.ConnectionStore, log *slog.Logger, guard *Guard) *Connector {
	ensureProxyAgent(ctx, data)
	connector := newConn(data, log, guard)
	conns.AddAgent(connector)
	orgs, err := data.ListOrgs(ctx)
	if err != nil {
		return connector
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
	return connector
}

func ensureProxyAgent(ctx context.Context, data store.DataStore) {
	if _, err := data.GetOrg(ctx, SystemOrgID); err != nil {
		_ = data.CreateOrg(ctx, &store.Org{ID: SystemOrgID, Name: "system", CreatedAt: time.Now()})
	}
	if _, err := data.GetAgent(ctx, AgentID); err != nil {
		_ = data.CreateAgent(ctx, &store.Agent{ID: AgentID, OrgID: SystemOrgID, Name: "agentless-proxy", Status: store.AgentActive, CreatedAt: time.Now()})
	}
}

func (c *Connector) ClusterLocal() bool { return true }
