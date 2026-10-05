package cluster

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/mishmesh/mishmesh/internal/store"
)

const defaultDialTimeout = 3 * time.Second

type peer struct {
	mu   sync.Mutex
	sess *yamux.Session
}

type Client struct {
	secret      []byte
	dialTimeout time.Duration
	now         func() time.Time

	mu     sync.Mutex
	peers  map[string]*peer
	closed bool
}

func NewClient(secret []byte) *Client {
	return &Client{secret: secret, dialTimeout: defaultDialTimeout, now: time.Now, peers: make(map[string]*peer)}
}

func (c *Client) peerFor(addr string) (*peer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("cluster: client closed")
	}
	p, ok := c.peers[addr]
	if !ok {
		p = &peer{}
		c.peers[addr] = p
	}
	return p, nil
}

func (c *Client) session(ctx context.Context, addr string) (*yamux.Session, error) {
	p, err := c.peerFor(addr)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess != nil && !p.sess.IsClosed() {
		return p.sess, nil
	}
	sess, err := c.connect(ctx, addr)
	if err != nil {
		return nil, err
	}
	p.sess = sess
	return sess, nil
}

func (c *Client) drop(addr string, sess *yamux.Session) {
	_ = sess.Close()
	c.mu.Lock()
	p := c.peers[addr]
	c.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.sess == sess {
		p.sess = nil
	}
	p.mu.Unlock()
}

func (c *Client) connect(ctx context.Context, addr string) (*yamux.Session, error) {
	d := net.Dialer{Timeout: c.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("cluster: dial relay %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(c.dialTimeout))
	hello, err := NewHello(c.secret, c.now())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := writeHello(conn, hello); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || ack[0] != StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("cluster: relay %s rejected session: %w", addr, ErrRelayFailed)
	}
	_ = conn.SetDeadline(time.Time{})
	sess, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cluster: relay session %s: %w", addr, err)
	}
	return sess, nil
}

func (c *Client) Open(ctx context.Context, addr, agentID, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	conn, err := c.openOnce(ctx, addr, agentID, endpointID, kind, meta)
	if err == nil || !isSessionFailure(err) {
		return conn, err
	}
	return c.openOnce(ctx, addr, agentID, endpointID, kind, meta)
}

type sessionFailure struct{ err error }

func (s sessionFailure) Error() string { return s.err.Error() }
func (s sessionFailure) Unwrap() error { return s.err }

func isSessionFailure(err error) bool {
	_, ok := err.(sessionFailure)
	return ok
}

func (c *Client) openOnce(ctx context.Context, addr, agentID, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	sess, err := c.session(ctx, addr)
	if err != nil {
		return nil, err
	}
	stream, err := sess.OpenStream()
	if err != nil {
		c.drop(addr, sess)
		return nil, sessionFailure{fmt.Errorf("cluster: open relay stream %s: %w", addr, err)}
	}
	hdr := Header{AgentID: agentID, EndpointID: endpointID, Kind: kind, Meta: meta, Timestamp: c.now().Unix()}
	hdr.Sign(c.secret)
	deadline := time.Now().Add(openStreamTimeout + headerReadTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = stream.SetDeadline(deadline)
	if err := WriteHeader(stream, hdr); err != nil {
		_ = stream.Close()
		if sess.IsClosed() {
			c.drop(addr, sess)
			return nil, sessionFailure{err}
		}
		return nil, err
	}
	var status [1]byte
	if _, err := io.ReadFull(stream, status[:]); err != nil {
		_ = stream.Close()
		if sess.IsClosed() {
			c.drop(addr, sess)
			return nil, sessionFailure{fmt.Errorf("cluster: read relay status: %w", err)}
		}
		return nil, fmt.Errorf("cluster: read relay status: %w", err)
	}
	_ = stream.SetDeadline(time.Time{})
	switch status[0] {
	case StatusOK:
		return stream, nil
	case StatusNotHere:
		_ = stream.Close()
		return nil, ErrNotHere
	default:
		_ = stream.Close()
		return nil, ErrRelayFailed
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	peers := c.peers
	c.peers = make(map[string]*peer)
	c.mu.Unlock()
	for _, p := range peers {
		p.mu.Lock()
		if p.sess != nil {
			_ = p.sess.Close()
			p.sess = nil
		}
		p.mu.Unlock()
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
