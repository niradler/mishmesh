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
}

var _ store.AgentConn = (*agentConn)(nil)

func newAgentConn(agentID string, sess *tunnel.Session) *agentConn {
	return &agentConn{agentID: agentID, sess: sess}
}

func (a *agentConn) AgentID() string { return a.agentID }

func (a *agentConn) OpenStream(_ context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	return a.sess.OpenData(tunnel.StreamInit{EndpointID: endpointID, Kind: kind, Meta: meta})
}

func (a *agentConn) Close() error { return a.sess.Close() }
