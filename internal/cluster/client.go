package cluster

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const defaultDialTimeout = 3 * time.Second

type Client struct {
	secret      []byte
	dialTimeout time.Duration
	now         func() time.Time
}

func NewClient(secret []byte) *Client {
	return &Client{secret: secret, dialTimeout: defaultDialTimeout, now: time.Now}
}

func (c *Client) Open(ctx context.Context, addr, agentID, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	d := net.Dialer{Timeout: c.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("cluster: dial relay %s: %w", addr, err)
	}
	hdr := Header{AgentID: agentID, EndpointID: endpointID, Kind: kind, Meta: meta, Timestamp: c.now().Unix()}
	hdr.Sign(c.secret)
	deadline := time.Now().Add(openStreamTimeout + headerReadTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := WriteHeader(conn, hdr); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var status [1]byte
	if _, err := io.ReadFull(conn, status[:]); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cluster: read relay status: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	switch status[0] {
	case StatusOK:
		return conn, nil
	case StatusNotHere:
		_ = conn.Close()
		return nil, ErrNotHere
	default:
		_ = conn.Close()
		return nil, ErrRelayFailed
	}
}

type remoteConn struct {
	client  *Client
	agentID string
	addr    string
	onClose func()
}

var _ store.AgentConn = (*remoteConn)(nil)

func (c *Client) Remote(agentID, addr string, onClose func()) store.AgentConn {
	return &remoteConn{client: c, agentID: agentID, addr: addr, onClose: onClose}
}

func (r *remoteConn) AgentID() string { return r.agentID }

func (r *remoteConn) OpenStream(ctx context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	return r.client.Open(ctx, r.addr, r.agentID, endpointID, kind, meta)
}

func (r *remoteConn) Close() error {
	if r.onClose != nil {
		r.onClose()
	}
	return nil
}
