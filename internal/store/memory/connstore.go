package memory

import (
	"sync"

	"github.com/mishmesh/mishmesh/internal/store"
)

type ConnStore struct {
	mu        sync.RWMutex
	agents    map[string]store.AgentConn
	endpoints map[string]string
	usage     map[string]int64
}

func NewConnStore() *ConnStore {
	return &ConnStore{
		agents:    make(map[string]store.AgentConn),
		endpoints: make(map[string]string),
		usage:     make(map[string]int64),
	}
}

var _ store.ConnectionStore = (*ConnStore)(nil)

func (c *ConnStore) AddAgent(conn store.AgentConn) (superseded store.AgentConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := conn.AgentID()
	old := c.agents[id]
	c.agents[id] = conn
	return old
}

func (c *ConnStore) RemoveAgent(conn store.AgentConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	agentID := conn.AgentID()
	if c.agents[agentID] != conn {
		return
	}
	delete(c.agents, agentID)
	for ep, aid := range c.endpoints {
		if aid == agentID {
			delete(c.endpoints, ep)
		}
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

func (c *ConnStore) AddUsage(orgID string, bytes int64) {
	if orgID == "" || bytes == 0 {
		return
	}
	c.mu.Lock()
	c.usage[orgID] += bytes
	c.mu.Unlock()
}

func (c *ConnStore) Usage(orgID string) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.usage[orgID]
}
