package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

const AgentConnectPath = "/_mishmesh/agent/connect"

type DialOptions struct {
	Token      string
	HTTPClient *http.Client
}

func Dial(ctx context.Context, rawURL string, opts DialOptions) (net.Conn, error) {
	wsOpts := &websocket.DialOptions{}
	if opts.HTTPClient != nil {
		wsOpts.HTTPClient = opts.HTTPClient
	}
	if opts.Token != "" {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+opts.Token)
		wsOpts.HTTPHeader = h
	}
	c, resp, err := websocket.Dial(ctx, rawURL, wsOpts)
	if err != nil {
		if resp != nil {
			return nil, newHandshakeError(resp, err)
		}
		return nil, err
	}
	c.SetReadLimit(-1)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}

func Accept(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(-1)
	return websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil
}

type HandshakeError struct {
	StatusCode int
	Message    string
	err        error
}

func newHandshakeError(resp *http.Response, err error) *HandshakeError {
	msg := ""
	if resp.Body != nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		msg = strings.TrimSpace(string(b))
	}
	return &HandshakeError{StatusCode: resp.StatusCode, Message: msg, err: err}
}

func (e *HandshakeError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("handshake rejected with status %d", e.StatusCode)
	}
	return fmt.Sprintf("handshake rejected with status %d: %s", e.StatusCode, e.Message)
}

func (e *HandshakeError) Unwrap() error { return e.err }

func AsHandshakeError(err error) (*HandshakeError, bool) {
	var he *HandshakeError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}
