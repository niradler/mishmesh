package redis

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	presenceTTL    = 90 * time.Second
	redisOpTimeout = 200 * time.Millisecond
)

type ConnStore struct {
	rdb *goredis.Client

	mu        sync.RWMutex
	agents    map[string]store.AgentConn
	endpoints map[string]string

	usage *usageTracker
}

var _ store.ConnectionStore = (*ConnStore)(nil)

func NewConnStore(redisURL string, poolSize int) (*ConnStore, error) {
	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redis: parse url: %w", err)
	}
	if poolSize > 0 {
		opts.PoolSize = poolSize
	}
	rdb := goredis.NewClient(opts)
	return newWithClient(rdb), nil
}

func newWithClient(rdb *goredis.Client) *ConnStore {
	return &ConnStore{
		rdb:       rdb,
		agents:    make(map[string]store.AgentConn),
		endpoints: make(map[string]string),
		usage:     newUsageTracker(rdb),
	}
}

func (c *ConnStore) AddUsage(orgID string, bytes int64) { c.usage.add(orgID, bytes) }

func (c *ConnStore) Usage(orgID string) int64 { return c.usage.get(orgID) }

func (c *ConnStore) Close() error {
	c.usage.close()
	return c.rdb.Close()
}

func (c *ConnStore) AddAgent(conn store.AgentConn) (superseded store.AgentConn) {
	c.mu.Lock()
	id := conn.AgentID()
	old := c.agents[id]
	c.agents[id] = conn
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	key := fmt.Sprintf("mm:presence:%s", id)
	if err := c.rdb.Set(ctx, key, "1", presenceTTL).Err(); err != nil {
		slog.Warn("redis set presence failed", "agent_id", id, "err", err)
	}
	return old
}

func (c *ConnStore) RemoveAgent(conn store.AgentConn) {
	c.mu.Lock()
	agentID := conn.AgentID()
	if c.agents[agentID] != conn {
		c.mu.Unlock()
		return
	}
	delete(c.agents, agentID)
	for ep, aid := range c.endpoints {
		if aid == agentID {
			delete(c.endpoints, ep)
		}
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	key := fmt.Sprintf("mm:presence:%s", agentID)
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		slog.Warn("redis del presence failed", "agent_id", agentID, "err", err)
	}
}

func (c *ConnStore) GetAgent(agentID string) (store.AgentConn, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	conn, ok := c.agents[agentID]
	return conn, ok
}

func (c *ConnStore) BindEndpoint(endpointID, agentID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endpoints[endpointID] = agentID
}

func (c *ConnStore) UnbindEndpoint(endpointID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.endpoints, endpointID)
}

func (c *ConnStore) ResolveEndpoint(endpointID string) (store.AgentConn, bool) {
	c.mu.RLock()
	agentID, ok := c.endpoints[endpointID]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	conn, ok := c.agents[agentID]
	c.mu.RUnlock()
	return conn, ok
}

func (c *ConnStore) OwnedElsewhere(string) bool { return false }
