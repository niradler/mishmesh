package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxOriginTransports   = 64
	originMaxIdlePerHost  = 4
	originIdleConnTimeout = 90 * time.Second
	originIdleTimeout     = 60 * time.Second
)

type OriginateSpec struct {
	Name     string
	Listen   string
	Upstream string
	Token    string
}

type originTarget struct {
	scheme string
	host   string
	port   int
}

func (t originTarget) tls() bool {
	return t.scheme == "https"
}

func (t originTarget) authority() string {
	if (t.scheme == "https" && t.port == 443) || (t.scheme == "http" && t.port == 80) {
		if strings.Contains(t.host, ":") {
			return "[" + t.host + "]"
		}
		return t.host
	}
	return net.JoinHostPort(t.host, strconv.Itoa(t.port))
}

func parseOriginTarget(raw string) (originTarget, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return originTarget{}, errors.New("not a URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return originTarget{}, errors.New("must be scheme://host[:port] with no path, query or credentials")
	}
	return targetFromURL(u)
}

func targetFromURL(u *url.URL) (originTarget, error) {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return originTarget{}, errors.New("scheme must be http or https")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return originTarget{}, errors.New("host is required")
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return originTarget{}, errors.New("port must be 1-65535")
		}
		port = n
	}
	return originTarget{scheme: scheme, host: host, port: port}, nil
}

type transportKey struct {
	principal [sha256.Size]byte
	exit      string
	clientIP  string
}

type pooledTransport struct {
	rt   *http.Transport
	used time.Time
}

type originator struct {
	a     *Agent
	roots *x509.CertPool

	mu         sync.Mutex
	transports map[transportKey]*pooledTransport
}

func newOriginator(a *Agent, roots *x509.CertPool) *originator {
	return &originator{a: a, roots: roots, transports: map[transportKey]*pooledTransport{}}
}

func (o *originator) transport(creds egressCreds, clientIP string) *http.Transport {
	key := transportKey{principal: sha256.Sum256([]byte(creds.token)), exit: creds.exit, clientIP: clientIP}
	o.mu.Lock()
	defer o.mu.Unlock()
	if p, ok := o.transports[key]; ok {
		p.used = time.Now()
		return p.rt
	}
	if len(o.transports) >= maxOriginTransports {
		o.evictOldestLocked()
	}
	rt := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return o.a.openEgress(ctx, clientIP, addr, creds)
		},
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.roots},
		TLSHandshakeTimeout: egressHandshakeTimeout,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: originMaxIdlePerHost,
		IdleConnTimeout:     originIdleConnTimeout,
	}
	o.transports[key] = &pooledTransport{rt: rt, used: time.Now()}
	return rt
}

func (o *originator) evictOldestLocked() {
	var oldest transportKey
	var oldestAt time.Time
	found := false
	for k, p := range o.transports {
		if !found || p.used.Before(oldestAt) {
			oldest, oldestAt, found = k, p.used, true
		}
	}
	if found {
		o.transports[oldest].rt.CloseIdleConnections()
		delete(o.transports, oldest)
	}
}

func (o *originator) closeIdle() {
	o.mu.Lock()
	pooled := o.transports
	o.transports = map[transportKey]*pooledTransport{}
	o.mu.Unlock()
	for _, p := range pooled {
		p.rt.CloseIdleConnections()
	}
}

func (o *originator) serve(w http.ResponseWriter, r *http.Request, target originTarget, creds egressCreds, clientIP string, decrypted bool) {
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	if !policyPath(path) {
		http.Error(w, "egress refused: request path must be absolute with no dot, empty or ';' segments", http.StatusBadRequest)
		return
	}
	if r.Method != strings.ToUpper(r.Method) {
		http.Error(w, "egress refused: request method must be uppercase", http.StatusBadRequest)
		return
	}
	if creds.exit == "" && o.a.opts.Egress != nil {
		creds.exit = o.a.opts.Egress.Exit
	}
	inject, err := o.a.dec.checkRequest(r.Context(), egressRequest{
		host: target.host, port: target.port, method: r.Method, path: path,
		tls: target.tls(), decrypted: decrypted, auth: creds.token, exit: creds.exit,
	})
	if err != nil {
		o.refuse(w, target, err)
		return
	}
	if inject != nil && r.Method == http.MethodTrace {
		http.Error(w, "egress refused: credentials are never injected into TRACE", http.StatusForbidden)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.scheme
			pr.Out.URL.Host = target.authority()
			pr.Out.URL.User = nil
			pr.Out.Host = target.authority()
			for name := range pr.Out.Header {
				if strings.HasPrefix(strings.ToLower(name), "proxy-") {
					pr.Out.Header.Del(name)
				}
			}
			if inject != nil {
				pr.Out.Header.Del(inject.header)
				pr.Out.Header.Set(inject.header, inject.value)
			}
		},
		Transport:    o.transport(creds, clientIP),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) { o.upstreamFailed(w, target, err) },
		ErrorLog:     log.New(discardWriter{}, "", 0),
	}
	proxy.ServeHTTP(w, r)
}

func (o *originator) refuse(w http.ResponseWriter, target originTarget, err error) {
	var denied *DeniedError
	switch {
	case errors.As(err, &denied):
		http.Error(w, "egress refused: "+printable(denied.Reason, "policy_deny"), http.StatusForbidden)
	case errors.Is(err, errNotConnected):
		http.Error(w, "egress unavailable: "+errNotConnected.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, "egress decision unavailable", http.StatusServiceUnavailable)
	}
	o.a.log.Info("egress request refused", "destination", truncateLog(target.authority()), "err", err)
}

func (o *originator) upstreamFailed(w http.ResponseWriter, target originTarget, err error) {
	var refusal *egressRefusal
	status, reason := http.StatusBadGateway, "upstream request failed"
	switch {
	case errors.As(err, &refusal):
		status, reason = refusal.status, "egress "+refusal.reason
	case isCertificateError(err):
		reason = "upstream tls certificate verification failed for " + target.host
	case errors.Is(err, context.Canceled):
		reason = "client canceled the request"
	}
	o.a.log.Info("egress upstream failed", "destination", truncateLog(target.authority()), "status", status, "reason", reason)
	http.Error(w, reason, status)
}

func isCertificateError(err error) bool {
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

func policyPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00;") || strings.Contains(path, "//") {
		return false
	}
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func (o *originator) proxyHandler(clientIP string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isConnectUDP(r) {
			http.Error(w, "egress refused: "+errUDPEgress.Error(), http.StatusForbidden)
			return
		}
		if r.Method == http.MethodConnect {
			http.Error(w, "CONNECT must be the first request on a proxy connection", http.StatusMethodNotAllowed)
			return
		}
		creds, ok := proxyCredentials(r.Header.Get("Proxy-Authorization"))
		if !ok || (o.a.opts.Egress.RequireAuth && creds.token == "") {
			w.Header().Set("Proxy-Authenticate", `Basic realm="mishmesh"`)
			http.Error(w, "egress "+errProxyAuthRequired.reason, http.StatusProxyAuthRequired)
			return
		}
		if r.URL.Host == "" {
			http.Error(w, "proxy requests need an absolute http:// or https:// URL, or CONNECT", http.StatusBadRequest)
			return
		}
		target, err := targetFromURL(r.URL)
		if err != nil {
			http.Error(w, "egress refused: "+err.Error(), http.StatusBadRequest)
			return
		}
		o.serve(w, r, target, creds, clientIP, false)
	})
}

func (o *originator) fixedHandler(target originTarget, creds egressCreds) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect || isConnectUDP(r) {
			http.Error(w, "originate listeners forward plain requests only", http.StatusMethodNotAllowed)
			return
		}
		o.serve(w, r, target, creds, remoteHost(r.RemoteAddr), false)
	})
}

func (o *originator) server(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: egressHandshakeTimeout,
		IdleTimeout:       originIdleTimeout,
		MaxHeaderBytes:    maxProxyHeaderBytes,
		ErrorLog:          log.New(discardWriter{}, "", 0),
	}
}

func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return ""
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func newOneConnListener(c net.Conn) *oneConnListener {
	l := &oneConnListener{done: make(chan struct{})}
	l.conn = &notifyCloseConn{Conn: c, closed: l.done}
	return l
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	return nil
}

func (l *oneConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

type notifyCloseConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *notifyCloseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}
