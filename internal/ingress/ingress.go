package ingress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type Meter interface {
	AddBytes(kind string, in, out int64)
	HTTPRequest(code int)
}

type Options struct {
	Data             store.DataStore
	Conns            store.ConnectionStore
	Log              *slog.Logger
	BaseDomain       string
	Meter            Meter
	OIDCSignKey      []byte
	CookieSecure     bool
	OIDCAllowPrivate bool

	TrustedProxies          []*net.IPNet
	Limiter                 ratelimit.Limiter
	UpstreamResponseTimeout time.Duration
	LookupCacheTTL          time.Duration
	DisablePathRouting      bool
}

type Ingress struct {
	data        store.DataStore
	conns       store.ConnectionStore
	log         *slog.Logger
	apexHost    string
	meter       Meter
	oidc        *oidcGate
	pathRouting bool
	trusted     []*net.IPNet
	limiter     ratelimit.Limiter
	proxy       *httputil.ReverseProxy

	endpoints *ttlCache[*store.Endpoint]
	quotas    *ttlCache[*store.Quota]
	basicAuth *ttlCache[struct{}]
}

func New(opts Options) *Ingress {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	i := &Ingress{
		data:        opts.Data,
		conns:       opts.Conns,
		log:         log,
		apexHost:    hostOnly(opts.BaseDomain),
		meter:       opts.Meter,
		trusted:     opts.TrustedProxies,
		limiter:     opts.Limiter,
		pathRouting: !opts.DisablePathRouting,

		endpoints: newTTLCache[*store.Endpoint](opts.LookupCacheTTL),
		quotas:    newTTLCache[*store.Quota](opts.LookupCacheTTL),
		basicAuth: newTTLCache[struct{}](basicAuthCacheTTL),
	}
	if i.limiter == nil {
		i.limiter = ratelimit.NewMemory()
	}
	i.proxy = i.newProxy(opts.UpstreamResponseTimeout)
	if len(opts.OIDCSignKey) > 0 {
		i.oidc = newOIDCGate(opts.Data, opts.OIDCSignKey, opts.CookieSecure, false, opts.OIDCAllowPrivate, log)
	}
	return i
}

func (i *Ingress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == oidcCallbackPath {
		if i.oidc == nil {
			http.Error(w, "oidc not configured", http.StatusServiceUnavailable)
			return
		}
		i.oidc.handleCallback(w, r)
		return
	}
	ep, outPath, err := i.resolve(r)
	if err != nil {
		i.writeResolveError(w, err)
		return
	}
	limit, exceeded := i.bandwidthLimit(r, ep)
	if exceeded {
		http.Error(w, "bandwidth quota exceeded", http.StatusTooManyRequests)
		i.recordCode(http.StatusTooManyRequests)
		return
	}
	if !applyPolicyGate(w, r, ep, i.gateDeps()) {
		return
	}
	conn, ok := i.conns.ResolveEndpoint(ep.ID)
	if !ok {
		http.Error(w, "tunnel offline: no agent is connected for this endpoint", http.StatusServiceUnavailable)
		i.recordCode(http.StatusServiceUnavailable)
		return
	}
	if isUpgrade(r) {
		i.proxyUpgrade(w, r, conn, ep, outPath, limit)
		return
	}
	i.proxyHTTP(w, r, conn, ep, outPath, limit)
}

func (i *Ingress) gateDeps() gateDeps {
	return gateDeps{oidc: i.oidc, trusted: i.trusted, limiter: i.limiter, basic: i.basicAuth}
}

var (
	errTunnelNotFound   = errors.New("tunnel not found")
	errStoreUnavailable = errors.New("store unavailable")
)

func (i *Ingress) writeResolveError(w http.ResponseWriter, err error) {
	if errors.Is(err, errStoreUnavailable) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "temporarily unavailable, retry shortly", http.StatusServiceUnavailable)
		i.recordCode(http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "tunnel not found", http.StatusNotFound)
}

func (i *Ingress) lookupFailed(op string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errTunnelNotFound
	}
	i.log.Error("endpoint lookup failed", "op", op, "err", err)
	return errStoreUnavailable
}

func (i *Ingress) resolve(r *http.Request) (ep *store.Endpoint, outPath string, err error) {
	host := hostOnly(r.Host)
	if sub, isSub := i.subdomain(host); isSub {
		e, err := i.endpointBySubdomain(r.Context(), sub)
		if err != nil {
			return nil, "", i.lookupFailed("subdomain", err)
		}
		return e, r.URL.Path, nil
	}
	if host != "" && host != i.apexHost {
		e, err := i.endpointByDomain(r.Context(), host)
		if err == nil {
			return e, r.URL.Path, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, "", i.lookupFailed("domain", err)
		}
	}
	if id, rest, isPath := pathEndpoint(r.URL.Path); isPath && i.pathRouting {
		e, err := i.endpointByID(r.Context(), id)
		if err != nil {
			return nil, "", i.lookupFailed("id", err)
		}
		return e, rest, nil
	}
	return nil, "", errTunnelNotFound
}

func pathEndpoint(p string) (id, outPath string, ok bool) {
	const prefix = "/tunnel/"
	if !strings.HasPrefix(p, prefix) {
		return "", "", false
	}
	id, rest, _ := strings.Cut(strings.TrimPrefix(p, prefix), "/")
	if id == "" {
		return "", "", false
	}
	return id, "/" + rest, true
}

func (i *Ingress) subdomain(host string) (string, bool) {
	h := hostOnly(host)
	if h == "" || h == i.apexHost {
		return "", false
	}
	suffix := "." + i.apexHost
	if !strings.HasSuffix(h, suffix) {
		return "", false
	}
	label := strings.TrimSuffix(h, suffix)
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

func (i *Ingress) meterTargetFor(ep *store.Endpoint, kind string, limit int64) meterTarget {
	return meterTarget{conns: i.conns, meter: i.meter, orgID: ep.OrgID, kind: kind, limit: limit}
}

func (i *Ingress) proxyHTTP(w http.ResponseWriter, r *http.Request, conn store.AgentConn, ep *store.Endpoint, outPath string, limit int64) {
	u := &upstream{
		agent:   conn,
		ep:      ep,
		outPath: outPath,
		target:  i.meterTargetFor(ep, store.KindHTTP, limit),
		trusted: i.trusted,
	}
	i.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), upstreamKey{}, u)))
}

func (i *Ingress) proxyUpgrade(w http.ResponseWriter, r *http.Request, conn store.AgentConn, ep *store.Endpoint, outPath string, limit int64) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "upgrade unsupported", http.StatusInternalServerError)
		return
	}
	raw, err := conn.OpenStream(r.Context(), ep.ID, store.KindHTTP, nil)
	if err != nil {
		http.Error(w, "tunnel offline: could not open a stream to the agent", http.StatusServiceUnavailable)
		i.recordCode(http.StatusServiceUnavailable)
		i.log.Warn("open stream failed", "endpoint_id", ep.ID, "err", err)
		return
	}
	stream := newMeteredConn(raw, i.meterTargetFor(ep, store.KindHTTP, limit))
	defer stream.Close()
	stop := context.AfterFunc(r.Context(), func() { _ = stream.Close() })
	defer stop()

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	applyOutboundPath(outReq, ep, outPath)
	if ep.Policy != nil && ep.Policy.HostHeader != "" {
		outReq.Host = ep.Policy.HostHeader
	}
	stripHopHeaders(outReq.Header)
	preserveUpgradeHeaders(outReq.Header, r.Header)
	setForwardedHeaders(r, outReq, i.trusted)
	stripGateCredentials(outReq.Header, ep)
	applyRequestPolicy(outReq, ep)
	if err := outReq.Write(stream); err != nil {
		http.Error(w, upstreamUnreachableMessage(""), http.StatusBadGateway)
		i.recordCode(http.StatusBadGateway)
		return
	}

	br := bufio.NewReader(stream)
	if reason, failed := tunnel.ReadStreamError(br); failed {
		http.Error(w, upstreamUnreachableMessage(reason), http.StatusBadGateway)
		i.recordCode(http.StatusBadGateway)
		return
	}

	client, clientBuf, err := hj.Hijack()
	if err != nil {
		i.log.Warn("hijack failed", "endpoint_id", ep.ID, "err", err)
		return
	}
	defer client.Close()

	tunnel.Splice(&bufferedConn{Conn: client, r: clientBuf.Reader}, &bufferedConn{Conn: stream, r: br})
}

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *bufferedConn) CloseWrite() error { return tunnel.CloseWrite(b.Conn) }

func applyOutboundPath(out *http.Request, ep *store.Endpoint, outPath string) {
	if ep.Policy != nil && ep.Policy.StripPathPrefix != "" {
		outPath = strings.TrimPrefix(outPath, ep.Policy.StripPathPrefix)
		if !strings.HasPrefix(outPath, "/") {
			outPath = "/" + outPath
		}
	}
	if ep.Policy != nil && ep.Policy.AddPathPrefix != "" {
		outPath = strings.TrimRight(ep.Policy.AddPathPrefix, "/") + outPath
	}
	out.URL.Path = outPath
	out.URL.RawPath = ""
}

func preserveUpgradeHeaders(dst, src http.Header) {
	if u := src.Get("Upgrade"); u != "" {
		dst.Set("Upgrade", u)
		dst.Set("Connection", "Upgrade")
	}
	for _, k := range []string{"Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions"} {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

func (i *Ingress) bandwidthLimit(r *http.Request, ep *store.Endpoint) (limit int64, exceeded bool) {
	if ep == nil || ep.OrgID == "" {
		return 0, false
	}
	q, err := i.quotaFor(r.Context(), ep.OrgID)
	if err != nil || q.MaxBandwidthBytes <= 0 {
		return 0, false
	}
	return q.MaxBandwidthBytes, i.conns.Usage(ep.OrgID) >= q.MaxBandwidthBytes
}

func (i *Ingress) recordCode(code int) {
	if i.meter != nil {
		i.meter.HTTPRequest(code)
	}
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}

var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func stripHopHeaders(h http.Header) {
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
