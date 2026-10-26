package egress

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	extensionECH       = 0xfe0d
	clientHelloTimeout = 10 * time.Second
	maxClientHelloSize = 64 << 10
)

var (
	ErrSNIMismatch        = errors.New("egress: tls server name does not match the destination host")
	ErrECHOuterMismatch   = errors.New("egress: tls encrypted client hello hides the server name and its outer name does not match the destination host")
	ErrMalformedHello     = errors.New("egress: malformed tls client hello")
	errClientHelloInspect = errors.New("egress: client hello inspected")
)

type sniConn struct {
	net.Conn
	host      string
	inspected bool
	pending   []byte
	refusal   atomic.Pointer[string]
}

func newSNIConn(c net.Conn, host string) *sniConn {
	return &sniConn{Conn: c, host: host}
}

func (c *sniConn) Read(p []byte) (int, error) {
	if !c.inspected {
		c.inspected = true
		buf, err := c.inspect()
		if err != nil {
			switch {
			case errors.Is(err, ErrECHOuterMismatch):
				c.refuse(reasonECH)
			case errors.Is(err, ErrSNIMismatch):
				c.refuse(reasonSNI)
			}
			return 0, err
		}
		c.pending = buf
	}
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func (c *sniConn) CloseWrite() error {
	return tunnel.CloseWrite(c.Conn)
}

func (c *sniConn) refuse(reason string) {
	c.refusal.Store(&reason)
}

func (c *sniConn) mismatch() string {
	if reason := c.refusal.Load(); reason != nil {
		return *reason
	}
	return ""
}

func (c *sniConn) inspect() ([]byte, error) {
	first := make([]byte, 1)
	if _, err := io.ReadFull(c.Conn, first); err != nil {
		return nil, err
	}
	if first[0] != tunnel.TLSHandshakeRecord {
		return first, nil
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(clientHelloTimeout))
	defer func() { _ = c.Conn.SetReadDeadline(time.Time{}) }()
	var seen bytes.Buffer
	seen.Write(first)
	src := io.TeeReader(io.LimitReader(c.Conn, maxClientHelloSize), &seen)
	hello, ok := readClientHello(io.MultiReader(bytes.NewReader(first), src))
	if !ok {
		return nil, ErrMalformedHello
	}
	if hello.serverName == "" && net.ParseIP(c.host) == nil || hello.serverName != "" && !sameHost(hello.serverName, c.host) {
		if hello.ech {
			return nil, ErrECHOuterMismatch
		}
		return nil, ErrSNIMismatch
	}
	return seen.Bytes(), nil
}

func sameHost(serverName, host string) bool {
	return strings.TrimSuffix(strings.ToLower(serverName), ".") == host
}

type clientHello struct {
	serverName string
	ech        bool
}

func readClientHello(r io.Reader) (clientHello, bool) {
	var hello clientHello
	var seen bool
	cfg := &tls.Config{
		GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) {
			hello = clientHello{serverName: info.ServerName, ech: slices.Contains(info.Extensions, extensionECH)}
			seen = true
			return nil, errClientHelloInspect
		},
	}
	_ = tls.Server(readOnlyConn{r: r}, cfg).Handshake()
	return hello, seen
}

type readOnlyConn struct {
	r io.Reader
}

func (c readOnlyConn) Read(p []byte) (int, error)     { return c.r.Read(p) }
func (readOnlyConn) Write(p []byte) (int, error)      { return len(p), nil }
func (readOnlyConn) Close() error                     { return nil }
func (readOnlyConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (readOnlyConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (readOnlyConn) SetDeadline(time.Time) error      { return nil }
func (readOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (readOnlyConn) SetWriteDeadline(time.Time) error { return nil }
