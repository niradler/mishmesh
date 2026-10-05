package gateway

import (
	"context"
	"net"
	"sync"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type agentConn struct {
	agentID string
	sess    *tunnel.Session
	metrics Metrics
}

var _ store.AgentConn = (*agentConn)(nil)

func newAgentConn(agentID string, sess *tunnel.Session, metrics Metrics) *agentConn {
	return &agentConn{agentID: agentID, sess: sess, metrics: metrics}
}

func (a *agentConn) AgentID() string { return a.agentID }

func (a *agentConn) OpenStream(_ context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	conn, err := a.sess.OpenData(tunnel.StreamInit{EndpointID: endpointID, Kind: kind, Meta: meta})
	if err != nil {
		return nil, err
	}
	if a.metrics == nil {
		return conn, nil
	}
	a.metrics.StreamOpened(kind)
	return &trackedConn{Conn: conn, onClose: func() { a.metrics.StreamClosed(kind) }}, nil
}

type trackedConn struct {
	net.Conn
	once    sync.Once
	onClose func()
}

func (c *trackedConn) Close() error {
	c.once.Do(c.onClose)
	return c.Conn.Close()
}

func (c *trackedConn) CloseWrite() error { return tunnel.CloseWrite(c.Conn) }

func (a *agentConn) Close() error { return a.sess.Close() }
