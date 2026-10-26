package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	DefaultEgressListen    = "127.0.0.1:3128"
	egressHandshakeTimeout = 10 * time.Second
	exitDialTimeout        = 10 * time.Second
	exitDialBudget         = 30 * time.Second
	maxProxyHeaderBytes    = 32 << 10
	proxyExitUserPrefix    = "exit="
	connectUDPProtocol     = "connect-udp"
	masqueUDPPathPrefix    = "/.well-known/masque/udp/"
)

type EgressSpec struct {
	Listen      string
	SocksListen string
	RequireAuth bool
	Exit        string
	ServeExit   bool
	ExitAllow   []string
	Originate   []OriginateSpec
}

type ExitDialer interface {
	Dial(ctx context.Context, target string, otls *store.OriginTLS, timeout time.Duration) (net.Conn, error)
	ResolveAll(ctx context.Context, target string) ([]string, error)
}

var (
	errExitAborted     = errors.New("egress exit: server aborted the approval")
	errExitNotApproved = errors.New("egress exit: server approved targets this exit did not resolve")
)

type egressCreds struct {
	token string
	exit  string
}

type egressRefusal struct {
	status int
	reason string
}

func (e *egressRefusal) Error() string {
	return fmt.Sprintf("egress refused (%d): %s", e.status, e.reason)
}

var errProxyAuthRequired = &egressRefusal{status: http.StatusProxyAuthRequired, reason: "proxy credentials required"}

var egressStatusCodes = map[tunnel.EgressStatus]int{
	tunnel.EgressDenied:       http.StatusForbidden,
	tunnel.EgressUnavailable:  http.StatusServiceUnavailable,
	tunnel.EgressDialFailed:   http.StatusBadGateway,
	tunnel.EgressInvalid:      http.StatusBadRequest,
	tunnel.EgressAuthRequired: http.StatusProxyAuthRequired,
}

func (a *Agent) listenEgress(ctx context.Context, spec EgressSpec, wg *sync.WaitGroup) (func(), error) {
	var listeners []net.Listener
	closeAll := func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	for _, l := range []struct {
		addr  string
		kind  string
		serve func(context.Context, net.Conn)
	}{
		{spec.Listen, "http", a.serveEgressClient},
		{spec.SocksListen, "socks5", a.serveSocksClient},
	} {
		if l.addr == "" {
			continue
		}
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			closeAll()
			return nil, err
		}
		listeners = append(listeners, ln)
		serve := l.serve
		wg.Go(func() {
			for {
				client, err := ln.Accept()
				if err != nil {
					return
				}
				go serve(ctx, client)
			}
		})
		a.log.Info("egress proxy listening", "proto", l.kind, "addr", ln.Addr().String(), "require_auth", spec.RequireAuth)
	}
	for _, o := range spec.Originate {
		target, err := parseOriginTarget(o.Upstream)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("originate %s: upstream %w", o.Name, err)
		}
		ln, err := net.Listen("tcp", o.Listen)
		if err != nil {
			closeAll()
			return nil, err
		}
		listeners = append(listeners, ln)
		srv := a.origin.server(a.origin.fixedHandler(target, egressCreds{token: o.Token}))
		wg.Go(func() { _ = srv.Serve(ln) })
		a.log.Info("egress originate listening", "name", o.Name, "addr", ln.Addr().String(), "upstream", target.scheme+"://"+target.authority())
	}
	return closeAll, nil
}

func (a *Agent) serveEgressClient(ctx context.Context, client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(egressHandshakeTimeout))
	limited := &io.LimitedReader{R: client, N: maxProxyHeaderBytes}
	rec := &recordingReader{r: limited, on: true}
	br := bufio.NewReader(rec)
	req, err := http.ReadRequest(br)
	rec.on = false
	if err != nil {
		writeProxyError(client, http.StatusBadRequest, "malformed proxy request")
		return
	}
	limited.N = math.MaxInt64
	creds, ok := proxyCredentials(req.Header.Get("Proxy-Authorization"))
	if !ok || (a.opts.Egress.RequireAuth && creds.token == "") {
		writeEgressFailure(client, errProxyAuthRequired)
		return
	}
	if isConnectUDP(req) {
		writeProxyError(client, http.StatusForbidden, "egress refused: "+errUDPEgress.Error())
		a.log.Info("egress refused", "proto", "connect-udp", "err", errUDPEgress)
		return
	}
	if req.Method == http.MethodConnect {
		a.serveConnect(ctx, client, br, req, creds)
		return
	}
	_ = client.SetDeadline(time.Time{})
	a.serveProxyHTTP(client, rec.buf.Bytes())
}

func isConnectUDP(req *http.Request) bool {
	for _, upgrade := range req.Header.Values("Upgrade") {
		for proto := range strings.SplitSeq(upgrade, ",") {
			if strings.EqualFold(strings.TrimSpace(proto), connectUDPProtocol) {
				return true
			}
		}
	}
	return req.URL != nil && strings.HasPrefix(req.URL.Path, masqueUDPPathPrefix)
}

func proxyCredentials(header string) (egressCreds, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return egressCreds{}, true
	}
	scheme, value, found := strings.Cut(header, " ")
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return egressCreds{}, false
	}
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		return egressCreds{token: value}, true
	case strings.EqualFold(scheme, "Basic"):
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return egressCreds{}, false
		}
		user, pass, found := strings.Cut(string(raw), ":")
		if !found {
			return egressCreds{}, false
		}
		return credsFromUserPass(user, pass), true
	}
	return egressCreds{}, false
}

func credsFromUserPass(user, pass string) egressCreds {
	creds := egressCreds{token: pass}
	if exit, ok := strings.CutPrefix(user, proxyExitUserPrefix); ok {
		creds.exit = exit
	}
	return creds
}

func (a *Agent) serveConnect(ctx context.Context, client net.Conn, br *bufio.Reader, req *http.Request, creds egressCreds) {
	dest := req.Host
	if _, port, err := net.SplitHostPort(dest); err != nil || port == "" {
		writeProxyError(client, http.StatusBadRequest, "CONNECT target must be host:port")
		return
	}
	if a.serveDecrypted(client, br, dest, creds) {
		return
	}
	stream, err := a.openEgress(ctx, remoteHost(client.RemoteAddr().String()), dest, creds)
	if err != nil {
		writeEgressFailure(client, err)
		a.log.Info("egress refused", "destination", truncateLog(dest), "err", err)
		return
	}
	defer stream.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	tunnel.Splice(&tunnel.BufferedConn{Conn: client, R: br}, stream)
}

func (a *Agent) serveProxyHTTP(client net.Conn, head []byte) {
	replay := &replayConn{Conn: client, head: head}
	srv := a.origin.server(a.origin.proxyHandler(remoteHost(client.RemoteAddr().String())))
	_ = srv.Serve(newOneConnListener(replay))
}

type replayConn struct {
	net.Conn
	head []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.head) > 0 {
		n := copy(p, c.head)
		c.head = c.head[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

type recordingReader struct {
	r   io.Reader
	buf bytes.Buffer
	on  bool
}

func (r *recordingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if r.on && n > 0 {
		r.buf.Write(p[:n])
	}
	return n, err
}

func (a *Agent) openEgress(ctx context.Context, clientIP, dest string, creds egressCreds) (net.Conn, error) {
	sess := a.sess.Load()
	if sess == nil {
		return nil, &egressRefusal{status: http.StatusServiceUnavailable, reason: errNotConnected.Error()}
	}
	meta := map[string]string{tunnel.MetaEgress: dest}
	if clientIP != "" {
		meta[tunnel.MetaEgressClient] = clientIP
	}
	if creds.token != "" {
		meta[tunnel.MetaEgressAuth] = creds.token
	}
	via := creds.exit
	if via == "" {
		via = a.opts.Egress.Exit
	}
	if via != "" {
		meta[tunnel.MetaEgressVia] = via
	}
	stream, err := sess.OpenData(tunnel.StreamInit{Kind: store.KindTCP, Meta: meta})
	if err != nil {
		return nil, &egressRefusal{status: http.StatusServiceUnavailable, reason: "tunnel stream failed"}
	}
	stopWatch := context.AfterFunc(ctx, func() { _ = stream.Close() })
	_ = stream.SetReadDeadline(time.Now().Add(tunnel.EgressReplyTimeout))
	reply, err := tunnel.ReadEgressReply(stream)
	_ = stream.SetReadDeadline(time.Time{})
	if !stopWatch() {
		_ = stream.Close()
		return nil, &egressRefusal{status: http.StatusServiceUnavailable, reason: "request canceled"}
	}
	if err != nil {
		_ = stream.Close()
		return nil, &egressRefusal{status: http.StatusBadGateway, reason: "no egress reply from gateway"}
	}
	if reply.Status != tunnel.EgressOK {
		_ = stream.Close()
		code, ok := egressStatusCodes[reply.Status]
		if !ok {
			code = http.StatusBadGateway
		}
		return nil, &egressRefusal{status: code, reason: printable(reply.Reason, reply.Status.String())}
	}
	return stream, nil
}

func exitGuard(allow []string, log *slog.Logger) ExitDialer {
	g, err := proxy.NewGuard(false, false, allow)
	if err != nil {
		log.Warn("egress exit_allow ignored, using the default guard", "err", err)
		return proxy.DefaultGuard()
	}
	return g
}

func (a *Agent) serveExit(ctx context.Context, stream net.Conn, dest string, reportResolved bool) {
	refuse := func(status tunnel.EgressStatus, reason string) {
		_ = tunnel.WriteWithDeadline(stream, egressHandshakeTimeout, func() error {
			return tunnel.WriteEgressReply(stream, tunnel.EgressReply{Status: status, Reason: reason})
		})
	}
	if a.opts.Egress == nil || !a.opts.Egress.ServeExit {
		a.log.Warn("egress exit request refused: serve_exit is off")
		refuse(tunnel.EgressDenied, "disabled")
		return
	}
	dialer := a.opts.ExitDialer
	if dialer == nil {
		dialer = proxy.DefaultGuard()
	}
	var upstream net.Conn
	var err error
	if reportResolved {
		upstream, err = dialApprovedExit(ctx, stream, dialer, dest)
	} else {
		upstream, err = dialer.Dial(ctx, dest, nil, exitDialTimeout)
	}
	if errors.Is(err, errExitAborted) {
		return
	}
	if err != nil {
		a.log.Info("egress exit dial refused", "destination", truncateLog(dest), "err", err)
		if errors.Is(err, errExitNotApproved) {
			refuse(tunnel.EgressInvalid, "unapproved_target")
			return
		}
		if errors.Is(err, proxy.ErrBlocked) {
			refuse(tunnel.EgressDenied, "blocked_destination")
			return
		}
		refuse(tunnel.EgressDialFailed, "dial_failed")
		return
	}
	defer upstream.Close()
	if err := tunnel.WriteWithDeadline(stream, egressHandshakeTimeout, func() error {
		return tunnel.WriteEgressReply(stream, tunnel.EgressReply{Status: tunnel.EgressOK})
	}); err != nil {
		return
	}
	tunnel.Splice(stream, upstream)
}

func dialApprovedExit(ctx context.Context, stream net.Conn, dialer ExitDialer, dest string) (net.Conn, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, exitDialTimeout)
	resolved, err := dialer.ResolveAll(resolveCtx, dest)
	cancel()
	if err != nil {
		return nil, err
	}
	if len(resolved) > tunnel.MaxExitTargets {
		resolved = resolved[:tunnel.MaxExitTargets]
	}
	err = tunnel.WriteWithDeadline(stream, egressHandshakeTimeout, func() error {
		return tunnel.WriteExitTargets(stream, resolved)
	})
	if err != nil {
		return nil, errExitAborted
	}
	approved, err := tunnel.ReadWithDeadline(stream, tunnel.EgressReplyTimeout, func() ([]string, error) {
		return tunnel.ReadExitTargets(stream)
	})
	if err != nil {
		return nil, errExitAborted
	}
	if len(approved) == 0 {
		return nil, errExitNotApproved
	}
	for _, target := range approved {
		if !slices.Contains(resolved, target) {
			return nil, errExitNotApproved
		}
	}
	var upstream net.Conn
	dialCtx, stopDial := context.WithTimeout(ctx, exitDialBudget)
	defer stopDial()
	for _, target := range approved {
		if upstream, err = dialer.Dial(dialCtx, target, nil, exitDialTimeout); err == nil {
			return upstream, nil
		}
	}
	return nil, err
}

func writeEgressFailure(w io.Writer, err error) {
	var refusal *egressRefusal
	if errors.As(err, &refusal) {
		writeProxyError(w, refusal.status, "egress "+refusal.reason)
		return
	}
	writeProxyError(w, http.StatusBadGateway, "egress failed")
}

func writeProxyError(w io.Writer, status int, msg string) {
	body := msg + "\n"
	challenge := ""
	if status == http.StatusProxyAuthRequired {
		challenge = "Proxy-Authenticate: Basic realm=\"mishmesh\"\r\n"
	}
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\n%sContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), challenge, len(body), body)
}

func printable(s, fallback string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, s)
	if clean == "" {
		return fallback
	}
	return clean
}

func truncateLog(s string) string {
	return tunnel.TruncateUTF8(s, 256)
}
