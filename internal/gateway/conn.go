package gateway

import (
	"context"
	"net"

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
	if err == nil && a.metrics != nil {
		a.metrics.StreamOpened(kind)
	}
	return conn, err
}

func (a *agentConn) Close() error { return a.sess.Close() }
