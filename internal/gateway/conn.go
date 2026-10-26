package gateway

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type agentConn struct {
	agentID string
	orgID   string
	sess    *tunnel.Session
	metrics Metrics
	ctrl    atomic.Pointer[tunnel.Control]

	boundOrg     string
	boundSession string
}

var errNoControl = errors.New("gateway: agent control stream not open")

var _ store.AgentConn = (*agentConn)(nil)

func newAgentConn(agentID, orgID string, sess *tunnel.Session, metrics Metrics) *agentConn {
	return &agentConn{agentID: agentID, orgID: orgID, sess: sess, metrics: metrics}
}

func (a *agentConn) AgentID() string { return a.agentID }

func (a *agentConn) setControl(ctrl *tunnel.Control) { a.ctrl.Store(ctrl) }

func (a *agentConn) Flush() error {
	ctrl := a.ctrl.Load()
	if ctrl == nil {
		return errNoControl
	}
	return ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgFlush})
}

type openResult struct {
	conn net.Conn
	err  error
}

func (a *agentConn) openData(ctx context.Context, init tunnel.StreamInit) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	done := make(chan openResult, 1)
	go func() {
		conn, err := a.sess.OpenData(init)
		done <- openResult{conn, err}
	}()
	select {
	case res := <-done:
		return res.conn, res.err
	case <-ctx.Done():
		go func() {
			if res := <-done; res.conn != nil {
				_ = res.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func (a *agentConn) OpenStream(ctx context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	conn, err := a.openData(ctx, tunnel.StreamInit{EndpointID: endpointID, Kind: kind, Meta: meta})
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
