package agent

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	DefaultEgressListen    = "127.0.0.1:3128"
	egressHandshakeTimeout = 10 * time.Second
	maxProxyHeaderBytes    = 32 << 10
)

type EgressSpec struct {
	Listen string
}

type egressRefusal struct {
	status int
	reason string
}

func (e *egressRefusal) Error() string {
	return fmt.Sprintf("egress refused (%d): %s", e.status, e.reason)
}

var egressStatusCodes = map[tunnel.EgressStatus]int{
	tunnel.EgressDenied:       http.StatusForbidden,
	tunnel.EgressUnavailable:  http.StatusServiceUnavailable,
	tunnel.EgressDialFailed:   http.StatusBadGateway,
	tunnel.EgressInvalid:      http.StatusBadRequest,
	tunnel.EgressAuthRequired: http.StatusProxyAuthRequired,
}

func (a *Agent) listenEgress(spec EgressSpec, wg *sync.WaitGroup) (func(), error) {
	ln, err := net.Listen("tcp", spec.Listen)
	if err != nil {
		return nil, err
	}
	wg.Go(func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go a.serveEgressClient(client)
		}
	})
	a.log.Info("egress proxy listening", "addr", ln.Addr().String())
	return func() { _ = ln.Close() }, nil
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
	if req.Method == http.MethodConnect {
		a.serveConnect(client, br, req)
		return
	}
	a.serveAbsoluteForm(client, req)
}

func (a *Agent) serveConnect(client net.Conn, br *bufio.Reader, req *http.Request) {
	dest := req.Host
	if _, port, err := net.SplitHostPort(dest); err != nil || port == "" {
		writeProxyError(client, http.StatusBadRequest, "CONNECT target must be host:port")
		return
	}
	stream, err := a.openEgress(dest)
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

func (a *Agent) serveAbsoluteForm(client net.Conn, req *http.Request) {
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
	stream, err := a.openEgress(dest)
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

func (a *Agent) openEgress(dest string) (net.Conn, error) {
	sess := a.sess.Load()
	if sess == nil {
		return nil, &egressRefusal{status: http.StatusServiceUnavailable, reason: errNotConnected.Error()}
	}
	stream, err := sess.OpenData(tunnel.StreamInit{Kind: store.KindTCP, Meta: map[string]string{tunnel.MetaEgress: dest}})
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
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
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
