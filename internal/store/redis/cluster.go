package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/cluster"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	defaultClusterTTL = 90 * time.Second
	clusterOpTimeout  = time.Second
	kickChannel       = "mm:kick"
	agentKeyPrefix    = "mm:agent:"
	endpointKeyPrefix = "mm:ep:"
)

var swapOwnerScript = goredis.NewScript(`
local old = redis.call('GET', KEYS[1])
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
return old
`)

var refreshOwnerScript = goredis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v == false then
  redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
  return 1
end
if v == ARGV[1] then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0
`)

var releaseOwnerScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

type ClusterOptions struct {
	NodeID    string
	Advertise string
	Relay     *cluster.Client
	TTL       time.Duration
	Log       *slog.Logger
}

type ownerInfo struct {
	Node string `json:"node"`
	Addr string `json:"addr"`
}

type kickMessage struct {
	AgentID string `json:"agent_id"`
	Node    string `json:"node"`
	NewNode string `json:"new_node,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

type ClusterConnStore struct {
	*ConnStore

	node      string
	selfValue string
	relay     *cluster.Client
	ttl       time.Duration
	log       *slog.Logger

	sub    *goredis.PubSub
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var (
	_ store.ConnectionStore = (*ClusterConnStore)(nil)
	_ cluster.LocalResolver = (*ClusterConnStore)(nil)
)

func NewClusterConnStore(ctx context.Context, redisURL string, opts ClusterOptions) (*ClusterConnStore, error) {
	ropts, err := goredis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redis: parse url: %w", err)
	}
	return newClusterWithClient(ctx, goredis.NewClient(ropts), opts)
}

func newClusterWithClient(ctx context.Context, rdb *goredis.Client, opts ClusterOptions) (*ClusterConnStore, error) {
	if opts.NodeID == "" || opts.Advertise == "" || opts.Relay == nil {
		return nil, errors.New("redis: cluster store requires node id, advertise address and relay client")
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultClusterTTL
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	self, err := json.Marshal(ownerInfo{Node: opts.NodeID, Addr: opts.Advertise})
	if err != nil {
		return nil, fmt.Errorf("redis: marshal owner info: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	c := &ClusterConnStore{
		ConnStore: newWithClient(rdb),
		node:      opts.NodeID,
		selfValue: string(self),
		relay:     opts.Relay,
		ttl:       ttl,
		log:       log,
		cancel:    cancel,
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		cancel()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	c.sub = rdb.Subscribe(runCtx, kickChannel)
	if _, err := c.sub.Receive(pingCtx); err != nil {
		cancel()
		_ = c.sub.Close()
		return nil, fmt.Errorf("redis: subscribe kick channel: %w", err)
	}

	c.wg.Add(2)
	go c.kickLoop(runCtx)
	go c.heartbeatLoop(runCtx)
	return c, nil
}

func (c *ClusterConnStore) Close() error {
	c.cancel()
	_ = c.sub.Close()
	c.wg.Wait()
	return c.rdb.Close()
}

func (c *ClusterConnStore) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), clusterOpTimeout)
}

func isClusterLocal(conn store.AgentConn) bool {
	cl, ok := conn.(store.ClusterLocal)
	return ok && cl.ClusterLocal()
}

func (c *ClusterConnStore) ttlSeconds() int {
	return int(c.ttl / time.Second)
}

func (c *ClusterConnStore) LocalAgent(agentID string) (store.AgentConn, bool) {
	return c.ConnStore.GetAgent(agentID)
}

func (c *ClusterConnStore) AddAgent(conn store.AgentConn) store.AgentConn {
	id := conn.AgentID()
	if !isClusterLocal(conn) {
		c.claimAgent(id)
	}
	c.mu.Lock()
	old := c.agents[id]
	c.agents[id] = conn
	c.mu.Unlock()
	return old
}

func (c *ClusterConnStore) claimAgent(agentID string) {
	ctx, cancel := c.opCtx()
	defer cancel()
	res, err := swapOwnerScript.Run(ctx, c.rdb, []string{agentKeyPrefix + agentID}, c.selfValue, c.ttlSeconds()).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		c.log.Warn("redis claim agent failed", "agent_id", agentID, "err", err)
		return
	}
	prev, _ := res.(string)
	if prev == "" || prev == c.selfValue {
		return
	}
	var prevOwner ownerInfo
	if json.Unmarshal([]byte(prev), &prevOwner) != nil || prevOwner.Node == c.node {
		return
	}
	c.publishKick(ctx, kickMessage{AgentID: agentID, Node: prevOwner.Node, NewNode: c.node})
}

func (c *ClusterConnStore) publishKick(ctx context.Context, msg kickMessage) {
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	if err := c.rdb.Publish(ctx, kickChannel, body).Err(); err != nil {
		c.log.Warn("redis publish kick failed", "agent_id", msg.AgentID, "err", err)
	}
}

func (c *ClusterConnStore) RemoveAgent(conn store.AgentConn) {
	agentID := conn.AgentID()
	c.mu.Lock()
	if c.agents[agentID] != conn {
		c.mu.Unlock()
		return
	}
	delete(c.agents, agentID)
	var boundEndpoints []string
	for ep, aid := range c.endpoints {
		if aid == agentID {
			boundEndpoints = append(boundEndpoints, ep)
			delete(c.endpoints, ep)
		}
	}
	c.mu.Unlock()

	if isClusterLocal(conn) {
		return
	}
	ctx, cancel := c.opCtx()
	defer cancel()
	released, err := releaseOwnerScript.Run(ctx, c.rdb, []string{agentKeyPrefix + agentID}, c.selfValue).Int()
	if err != nil {
		c.log.Warn("redis release agent failed", "agent_id", agentID, "err", err)
		return
	}
	if released == 0 {
		return
	}
	for _, ep := range boundEndpoints {
		if err := c.rdb.Del(ctx, endpointKeyPrefix+ep).Err(); err != nil {
			c.log.Warn("redis del endpoint binding failed", "endpoint_id", ep, "err", err)
		}
	}
}

func (c *ClusterConnStore) lookupOwner(ctx context.Context, agentID string) (ownerInfo, bool) {
	raw, err := c.rdb.Get(ctx, agentKeyPrefix+agentID).Result()
	if err != nil {
		if !errors.Is(err, goredis.Nil) {
			c.log.Warn("redis lookup agent owner failed", "agent_id", agentID, "err", err)
		}
		return ownerInfo{}, false
	}
	var info ownerInfo
	if json.Unmarshal([]byte(raw), &info) != nil || info.Node == "" {
		return ownerInfo{}, false
	}
	return info, true
}

func (c *ClusterConnStore) GetAgent(agentID string) (store.AgentConn, bool) {
	if conn, ok := c.ConnStore.GetAgent(agentID); ok {
		return conn, true
	}
	return c.remoteAgent(agentID)
}

func (c *ClusterConnStore) remoteAgent(agentID string) (store.AgentConn, bool) {
	ctx, cancel := c.opCtx()
	defer cancel()
	info, ok := c.lookupOwner(ctx, agentID)
	if !ok || info.Node == c.node {
		return nil, false
	}
	onClose := func() {
		kctx, kcancel := c.opCtx()
		defer kcancel()
		c.publishKick(kctx, kickMessage{AgentID: agentID, Node: info.Node, Force: true})
	}
	return c.relay.Remote(agentID, info.Addr, onClose), true
}

func (c *ClusterConnStore) BindEndpoint(endpointID, agentID string) {
	c.mu.Lock()
	c.endpoints[endpointID] = agentID
	virtual := false
	if conn, ok := c.agents[agentID]; ok {
		virtual = isClusterLocal(conn)
	}
	c.mu.Unlock()

	ttl := c.ttl
	if virtual {
		ttl = 0
	}
	ctx, cancel := c.opCtx()
	defer cancel()
	if err := c.rdb.Set(ctx, endpointKeyPrefix+endpointID, agentID, ttl).Err(); err != nil {
		c.log.Warn("redis bind endpoint failed", "endpoint_id", endpointID, "err", err)
	}
}

func (c *ClusterConnStore) UnbindEndpoint(endpointID string) {
	c.mu.Lock()
	delete(c.endpoints, endpointID)
	c.mu.Unlock()

	ctx, cancel := c.opCtx()
	defer cancel()
	if err := c.rdb.Del(ctx, endpointKeyPrefix+endpointID).Err(); err != nil {
		c.log.Warn("redis unbind endpoint failed", "endpoint_id", endpointID, "err", err)
	}
}

func (c *ClusterConnStore) ResolveEndpoint(endpointID string) (store.AgentConn, bool) {
	if conn, ok := c.ConnStore.ResolveEndpoint(endpointID); ok {
		return conn, true
	}
	ctx, cancel := c.opCtx()
	defer cancel()
	agentID, err := c.rdb.Get(ctx, endpointKeyPrefix+endpointID).Result()
	if err != nil {
		if !errors.Is(err, goredis.Nil) {
			c.log.Warn("redis resolve endpoint failed", "endpoint_id", endpointID, "err", err)
		}
		return nil, false
	}
	if conn, ok := c.ConnStore.GetAgent(agentID); ok {
		return conn, true
	}
	return c.remoteAgent(agentID)
}

func (c *ClusterConnStore) OwnedElsewhere(agentID string) bool {
	ctx, cancel := c.opCtx()
	defer cancel()
	info, ok := c.lookupOwner(ctx, agentID)
	return ok && info.Node != c.node
}

func (c *ClusterConnStore) kickLoop(ctx context.Context) {
	defer c.wg.Done()
	ch := c.sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			c.handleKick(m.Payload)
		}
	}
}

func (c *ClusterConnStore) handleKick(payload string) {
	var msg kickMessage
	if json.Unmarshal([]byte(payload), &msg) != nil || msg.Node != c.node {
		return
	}
	conn, ok := c.ConnStore.GetAgent(msg.AgentID)
	if !ok || isClusterLocal(conn) {
		return
	}
	if !msg.Force {
		ctx, cancel := c.opCtx()
		info, owned := c.lookupOwner(ctx, msg.AgentID)
		cancel()
		if owned && info.Node == c.node {
			return
		}
	}
	c.log.Info("closing local agent session after kick", "agent_id", msg.AgentID, "new_node", msg.NewNode, "force", msg.Force)
	_ = conn.Close()
}

func (c *ClusterConnStore) heartbeatLoop(ctx context.Context) {
	defer c.wg.Done()
	t := time.NewTicker(c.ttl / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.heartbeat(ctx)
		}
	}
}

func (c *ClusterConnStore) heartbeat(ctx context.Context) {
	c.mu.RLock()
	agents := make(map[string]store.AgentConn, len(c.agents))
	for id, conn := range c.agents {
		if !isClusterLocal(conn) {
			agents[id] = conn
		}
	}
	endpoints := make([]string, 0, len(c.endpoints))
	for ep, aid := range c.endpoints {
		if _, ok := agents[aid]; ok {
			endpoints = append(endpoints, ep)
		}
	}
	c.mu.RUnlock()

	for id, conn := range agents {
		opCtx, cancel := context.WithTimeout(ctx, clusterOpTimeout)
		owned, err := refreshOwnerScript.Run(opCtx, c.rdb, []string{agentKeyPrefix + id}, c.selfValue, c.ttlSeconds()).Int()
		cancel()
		if err != nil {
			c.log.Warn("redis heartbeat failed", "agent_id", id, "err", err)
			continue
		}
		if owned == 0 {
			c.log.Info("agent owned by another node; closing stale local session", "agent_id", id)
			_ = conn.Close()
		}
	}
	if len(endpoints) == 0 {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, clusterOpTimeout)
	defer cancel()
	pipe := c.rdb.Pipeline()
	for _, ep := range endpoints {
		pipe.Expire(opCtx, endpointKeyPrefix+ep, c.ttl)
	}
	if _, err := pipe.Exec(opCtx); err != nil {
		c.log.Warn("redis endpoint heartbeat failed", "err", err)
	}
}

func (c *ClusterConnStore) Drain(ctx context.Context) {
	c.mu.RLock()
	conns := make([]store.AgentConn, 0, len(c.agents))
	for _, conn := range c.agents {
		if !isClusterLocal(conn) {
			conns = append(conns, conn)
		}
	}
	c.mu.RUnlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
wait:
	for c.hasLocalSessions(conns) {
		select {
		case <-ctx.Done():
			break wait
		case <-deadline.C:
			break wait
		case <-poll.C:
		}
	}
	for _, conn := range conns {
		c.RemoveAgent(conn)
	}
}

func (c *ClusterConnStore) hasLocalSessions(conns []store.AgentConn) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, conn := range conns {
		if c.agents[conn.AgentID()] == conn {
			return true
		}
	}
	return false
}
