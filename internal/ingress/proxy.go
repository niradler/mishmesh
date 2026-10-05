package ingress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const defaultUpstreamResponseTimeout = 2 * time.Minute

var errAgentRefused = errors.New("agent could not reach the local service")

type upstreamKey struct{}

type upstream struct {
	agent   store.AgentConn
	ep      *store.Endpoint
	outPath string
	target  meterTarget
	trusted []*net.IPNet

	mu          sync.Mutex
	openErr     error
	agentReason string
	agentFailed bool
	metered     *meteredConn
}

func (u *upstream) open(ctx context.Context) (net.Conn, error) {
	stream, err := u.agent.OpenStream(ctx, u.ep.ID, store.KindHTTP, nil)
	if err != nil {
		u.mu.Lock()
		u.openErr = err
		u.mu.Unlock()
		return nil, err
	}
	mc := newMeteredConn(stream, u.target)
	u.mu.Lock()
	u.metered = mc
	u.mu.Unlock()
	return &upstreamConn{Conn: mc, br: bufio.NewReader(mc), u: u}, nil
}

func (u *upstream) setAgentFailure(reason string) {
	u.mu.Lock()
	u.agentFailed = true
	u.agentReason = reason
	u.mu.Unlock()
}

func (u *upstream) snapshot() (openErr error, agentFailed bool, reason string, exceeded bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.openErr, u.agentFailed, u.agentReason, u.metered != nil && u.metered.exceeded.Load()
}

type upstreamConn struct {
	net.Conn
	br      *bufio.Reader
	u       *upstream
	checked bool
}

func (c *upstreamConn) Read(p []byte) (int, error) {
	if !c.checked {
		c.checked = true
		if reason, failed := tunnel.ReadStreamError(c.br); failed {
			c.u.setAgentFailure(reason)
			return 0, errAgentRefused
		}
	}
	return c.br.Read(p)
}

func newUpstreamTransport(responseTimeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			u, ok := ctx.Value(upstreamKey{}).(*upstream)
			if !ok {
				return nil, errors.New("ingress: dial without upstream context")
			}
			return u.open(ctx)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: responseTimeout,
	}
}

func (i *Ingress) newProxy(responseTimeout time.Duration) *httputil.ReverseProxy {
	if responseTimeout <= 0 {
		responseTimeout = defaultUpstreamResponseTimeout
	}
	return &httputil.ReverseProxy{
		Transport:      newUpstreamTransport(responseTimeout),
		Rewrite:        i.rewriteRequest,
		ModifyResponse: i.modifyResponse,
		ErrorHandler:   i.proxyError,
		ErrorLog:       slog.NewLogLogger(i.log.Handler(), slog.LevelDebug),
	}
}

func upstreamOf(r *http.Request) *upstream {
	u, _ := r.Context().Value(upstreamKey{}).(*upstream)
	return u
}

func (i *Ingress) rewriteRequest(pr *httputil.ProxyRequest) {
	u := upstreamOf(pr.In)
	out := pr.Out
	out.URL.Scheme = "http"
	out.URL.Host = "tunnel.invalid"
	applyOutboundPath(out, u.ep, u.outPath)
	if ep := u.ep; ep.Policy != nil && ep.Policy.HostHeader != "" {
		out.Host = ep.Policy.HostHeader
	}
	setForwardedHeaders(pr.In, out, u.trusted)
	applyRequestPolicy(out, u.ep)
}

func setForwardedHeaders(in, out *http.Request, trusted []*net.IPNet) {
	peerTrusted := peerInNets(in.RemoteAddr, trusted)
	clientHost := in.RemoteAddr
	if h, _, err := net.SplitHostPort(in.RemoteAddr); err == nil {
		clientHost = h
	}
	xff := clientHost
	if peerTrusted {
		if prior := in.Header.Values("X-Forwarded-For"); len(prior) > 0 {
			xff = strings.Join(prior, ", ") + ", " + clientHost
		}
	}
	proto := "http"
	if in.TLS != nil {
		proto = "https"
	}
	host := in.Host
	if peerTrusted {
		if v := in.Header.Get("X-Forwarded-Proto"); v != "" {
			proto = v
		}
		if v := in.Header.Get("X-Forwarded-Host"); v != "" {
			host = v
		}
	}
	out.Header.Del("Forwarded")
	out.Header.Set("X-Forwarded-For", xff)
	out.Header.Set("X-Forwarded-Proto", proto)
	out.Header.Set("X-Forwarded-Host", host)
}

func ParseTrustedProxies(csv string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("invalid proxy address %q", part)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy CIDR %q: %w", part, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

func peerInNets(remoteAddr string, nets []*net.IPNet) bool {
	if len(nets) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (i *Ingress) modifyResponse(resp *http.Response) error {
	u := upstreamOf(resp.Request)
	applyResponsePolicy(resp.Header, u.ep)
	if shouldCompress(u.ep, resp.Request, resp) {
		resp.Header.Del("Content-Length")
		resp.Header.Set("Content-Encoding", "gzip")
		resp.Header.Add("Vary", "Accept-Encoding")
		resp.ContentLength = -1
		resp.Body = newGzipBody(resp.Body)
	}
	i.recordCode(resp.StatusCode)
	return nil
}

func (i *Ingress) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	u := upstreamOf(r)
	openErr, agentFailed, reason, exceeded := u.snapshot()

	var code int
	var msg string
	var maxBytes *http.MaxBytesError
	switch {
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		return
	case errors.As(err, &maxBytes):
		code, msg = http.StatusRequestEntityTooLarge, "request body too large"
	case exceeded || errors.Is(err, errBandwidthExceeded):
		code, msg = http.StatusTooManyRequests, "bandwidth quota exceeded"
	case openErr != nil:
		code, msg = http.StatusServiceUnavailable, "tunnel offline: could not open a stream to the agent"
		i.log.Warn("open stream failed", "endpoint_id", u.ep.ID, "err", openErr)
	case agentFailed:
		code, msg = http.StatusBadGateway, upstreamUnreachableMessage(reason)
	case isTimeout(err):
		code, msg = http.StatusGatewayTimeout, "timed out waiting for the upstream service to respond"
	default:
		code, msg = http.StatusBadGateway, upstreamUnreachableMessage("")
		i.log.Debug("upstream round trip failed", "endpoint_id", u.ep.ID, "err", err)
	}
	http.Error(w, msg, code)
	i.recordCode(code)
}

func upstreamUnreachableMessage(reason string) string {
	const base = "upstream service unreachable on the agent side"
	if reason == "" {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, reason)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

type gzipBody struct {
	src io.ReadCloser
	zw  *gzip.Writer
	buf bytes.Buffer
	raw []byte
	eof bool
}

func newGzipBody(src io.ReadCloser) *gzipBody {
	g := &gzipBody{src: src, raw: make([]byte, 32<<10)}
	g.zw = gzip.NewWriter(&g.buf)
	return g
}

func (g *gzipBody) Read(p []byte) (int, error) {
	for g.buf.Len() == 0 {
		if g.eof {
			return 0, io.EOF
		}
		n, err := g.src.Read(g.raw)
		if n > 0 {
			if _, werr := g.zw.Write(g.raw[:n]); werr != nil {
				return 0, werr
			}
			if ferr := g.zw.Flush(); ferr != nil {
				return 0, ferr
			}
		}
		if err == io.EOF {
			g.eof = true
			if cerr := g.zw.Close(); cerr != nil {
				return 0, cerr
			}
		} else if err != nil {
			return 0, err
		}
	}
	return g.buf.Read(p)
}

func (g *gzipBody) Close() error { return g.src.Close() }

func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		if strings.Contains(strings.ToLower(v), "upgrade") {
			return true
		}
	}
	return false
}
