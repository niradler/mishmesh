package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
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
	maxProxyHeaderBytes    = 32 << 10
	proxyExitUserPrefix    = "exit="
)

type EgressSpec struct {
	Listen      string
	SocksListen string
	RequireAuth bool
	Exit        string
	ServeExit   bool
}

type ExitDialer interface {
	Dial(ctx context.Context, target string, otls *store.OriginTLS, timeout time.Duration) (net.Conn, error)
}

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

func (a *Agent) listenEgress(spec EgressSpec, wg *sync.WaitGroup) (func(), error) {
	var listeners []net.Listener
	closeAll := func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	for _, l := range []struct {
		addr  string
		kind  string
		serve func(net.Conn)
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
				go serve(client)
			}
		})
		a.log.Info("egress proxy listening", "proto", l.kind, "addr", ln.Addr().String(), "require_auth", spec.RequireAuth)
	}
	return closeAll, nil
}

func (a *Agent) serveEgressClient(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(egressHandshakeTimeout))
	limited := &io.LimitedReader{R: client, N: maxProxyHeaderBytes}
	br := bufio.NewReader(limited)
	req, err := http.ReadRequest(br)
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
	if req.Method == http.MethodConnect {
		a.serveConnect(client, br, req, creds)
		return
	}
	a.serveAbsoluteForm(client, req, creds)
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

func (a *Agent) serveConnect(client net.Conn, br *bufio.Reader, req *http.Request, creds egressCreds) {
	dest := req.Host
	if _, port, err := net.SplitHostPort(dest); err != nil || port == "" {
		writeProxyError(client, http.StatusBadRequest, "CONNECT target must be host:port")
		return
	}
	stream, err := a.openEgress(dest, creds)
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
	tunnel.Splice(&bufferedConn{Conn: client, r: br}, stream)
}

func (a *Agent) serveAbsoluteForm(client net.Conn, req *http.Request, creds egressCreds) {
	if req.URL == nil || req.URL.Host == "" {
		writeProxyError(client, http.StatusBadRequest, "proxy requests need an absolute http:// URL or CONNECT")
		return
	}
	if !strings.EqualFold(req.URL.Scheme, "http") {
		writeProxyError(client, http.StatusBadRequest, "use CONNECT for "+req.URL.Scheme+" URLs")
		return
	}
	dest := req.URL.Host
	if _, _, err := net.SplitHostPort(dest); err != nil {
		dest = net.JoinHostPort(strings.Trim(req.URL.Hostname(), "[]"), "80")
	}
	stream, err := a.openEgress(dest, creds)
	if err != nil {
		writeEgressFailure(client, err)
		a.log.Info("egress refused", "destination", truncateLog(dest), "err", err)
		return
	}
	defer stream.Close()
	for name := range req.Header {
		if strings.HasPrefix(strings.ToLower(name), "proxy-") {
			req.Header.Del(name)
		}
	}
	req.RequestURI = ""
	req.URL.Scheme = ""
	req.URL.Host = ""
	req.Close = true
	_ = client.SetDeadline(time.Time{})
	if err := req.Write(stream); err != nil {
		return
	}
	_, _ = io.Copy(client, stream)
}

func (a *Agent) openEgress(dest string, creds egressCreds) (net.Conn, error) {
	sess := a.sess.Load()
	if sess == nil {
		return nil, &egressRefusal{status: http.StatusServiceUnavailable, reason: errNotConnected.Error()}
	}
	meta := map[string]string{tunnel.MetaEgress: dest}
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
	_ = stream.SetReadDeadline(time.Now().Add(tunnel.EgressReplyTimeout))
	reply, err := tunnel.ReadEgressReply(stream)
	_ = stream.SetReadDeadline(time.Time{})
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

func (a *Agent) serveExit(stream net.Conn, dest string) {
	refuse := func(status tunnel.EgressStatus, reason string) {
		_ = stream.SetWriteDeadline(time.Now().Add(egressHandshakeTimeout))
		_ = tunnel.WriteEgressReply(stream, tunnel.EgressReply{Status: status, Reason: reason})
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
	upstream, err := dialer.Dial(context.Background(), dest, nil, exitDialTimeout)
	if err != nil {
		a.log.Info("egress exit dial refused", "destination", truncateLog(dest), "err", err)
		if errors.Is(err, proxy.ErrBlocked) {
			refuse(tunnel.EgressDenied, "blocked_destination")
			return
		}
		refuse(tunnel.EgressDialFailed, "dial_failed")
		return
	}
	defer upstream.Close()
	_ = stream.SetWriteDeadline(time.Now().Add(egressHandshakeTimeout))
	if err := tunnel.WriteEgressReply(stream, tunnel.EgressReply{Status: tunnel.EgressOK}); err != nil {
		return
	}
	_ = stream.SetWriteDeadline(time.Time{})
	tunnel.Splice(stream, upstream)
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
	const maxLen = 256
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *bufferedConn) CloseWrite() error {
	return tunnel.CloseWrite(c.Conn)
}
