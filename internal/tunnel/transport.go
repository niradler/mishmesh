package tunnel

import (
	"context"
	"net"
	"net/http"

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
	c, _, err := websocket.Dial(ctx, rawURL, wsOpts)
	if err != nil {
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
