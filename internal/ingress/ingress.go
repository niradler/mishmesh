package ingress

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/flows"
	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/identity"
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
	Services                ServiceWaker
	Identity                *identity.Service
	Deciders                identity.Deciders
	APIPublicURL            string
	AccessLog               AccessRecorder
	Secrets                 SecretOpener
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
	services    ServiceWaker
	identity    *identity.Service
	deciders    identity.Deciders
	apiURL      string
	secure      bool
	access      AccessRecorder
	secrets     SecretOpener
	upgrades    *flows.Tracker

	endpoints *ttlCache[*store.Endpoint]
	quotas    *ttlCache[*store.Quota]
	misses    *ttlCache[struct{}]
	basicAuth *ttlCache[struct{}]
	caPools   *ttlCache[*x509.CertPool]
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
		services:    opts.Services,
		identity:    opts.Identity,
		deciders:    opts.Deciders,
		apiURL:      strings.TrimRight(opts.APIPublicURL, "/"),
		secure:      opts.CookieSecure,
		access:      opts.AccessLog,
		secrets:     opts.Secrets,

		endpoints: newTTLCache[*store.Endpoint](opts.LookupCacheTTL),
		quotas:    newTTLCache[*store.Quota](opts.LookupCacheTTL),
		misses:    newTTLCache[struct{}](min(opts.LookupCacheTTL, negativeCacheTTL)),
		basicAuth: newTTLCache[struct{}](basicAuthCacheTTL),
		caPools:   newTTLCache[*x509.CertPool](clientCAPoolCacheTTL),
	}
	if i.limiter == nil {
		i.limiter = ratelimit.NewMemory()
	}
	i.upgrades = newUpgradeTracker(i, opts.Identity)
	i.proxy = i.newProxy(opts.UpstreamResponseTimeout)
	if len(opts.OIDCSignKey) > 0 {
		i.oidc = newOIDCGate(opts.Data, opts.OIDCSignKey, opts.Identity, opts.CookieSecure, false, opts.OIDCAllowPrivate, opts.TrustedProxies, log)
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
	dropH2CUpgrade(r.Header)
	r = r.WithContext(context.WithValue(r.Context(), flushEpochKey{}, i.upgrades.Epoch()))
	ep, outPath, err := i.resolve(r)
	if err != nil {
		i.writeResolveError(w, err)
		return
	}
	start := time.Now()
	sw, r, rec := i.beginAccess(w, r)
	defer i.finishAccess(sw, r, ep, rec, start)
	i.serveEndpoint(sw, r, ep, outPath)
}

func (i *Ingress) serveEndpoint(w http.ResponseWriter, r *http.Request, ep *store.Endpoint, outPath string) {
	if ep.Kind != "" && ep.Kind != store.KindHTTP {
		i.writeResolveError(w, errTunnelNotFound)
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
	if outPath == identity.EndpointCallbackPath {
		i.handleEndpointCallback(w, r, ep)
		return
	}
	r, ok := i.applyIdentityGate(w, r, ep, outPath)
	if !ok {
		return
	}
	conn, online := i.conns.ResolveEndpoint(ep.ID)
	if !online {
		http.Error(w, "tunnel offline: no agent is connected for this endpoint", http.StatusServiceUnavailable)
		i.recordCode(http.StatusServiceUnavailable)
		return
	}
	release, proceed := i.wakeService(w, r, ep)
	if !proceed {
		return
	}
	defer release()
	accessFrom(r).markForwarded()
	if isUpgrade(r) {
		i.proxyUpgrade(w, r, conn, ep, outPath, limit)
		return
	}
	i.proxyHTTP(w, r, conn, ep, outPath, limit)
}

func (i *Ingress) gateDeps() gateDeps {
	return gateDeps{oidc: i.oidc, trusted: i.trusted, limiter: i.limiter, basic: i.basicAuth, caPools: i.caPools}
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
	if host != "" && host != i.apexHost {
		if _, err := connections.NormalizeAccessHostname(host); err != nil {
			return nil, "", errTunnelNotFound
		}
		endpoint, err := i.endpointByDomain(r.Context(), host)
		if err == nil {
			return endpoint, r.URL.Path, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, "", i.lookupFailed("domain", err)
		}
		endpoint, err = i.endpointByAccessHostname(r.Context(), host)
		if err == nil {
			return endpoint, r.URL.Path, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, "", i.lookupFailed("access hostname", err)
		}
	}
	if sub, isSub := i.subdomain(host); isSub {
		e, err := i.endpointBySubdomain(r.Context(), sub)
		if err != nil {
			return nil, "", i.lookupFailed("subdomain", err)
		}
		return e, r.URL.Path, nil
	}
	if host != "" && host != i.apexHost {
		return nil, "", errTunnelNotFound
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
	if r.ProtoMajor == 1 && isGRPCRequest(r) {
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			http.Error(w, "bidirectional streaming unavailable", http.StatusServiceUnavailable)
			i.recordCode(http.StatusServiceUnavailable)
			i.log.Warn("enable gRPC full duplex failed", "err", err)
			return
		}
	}
	u := &upstream{
		agent:       conn,
		ep:          ep,
		outPath:     outPath,
		target:      i.meterTargetFor(ep, store.KindHTTP, limit),
		trusted:     i.trusted,
		clientIP:    requestClientIP(r, i.trusted),
		clientHTTP1: r.ProtoMajor == 1,
	}
	i.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), upstreamKey{}, u)))
}

func (i *Ingress) proxyUpgrade(w http.ResponseWriter, r *http.Request, conn store.AgentConn, ep *store.Endpoint, outPath string, limit int64) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "upgrade unsupported", http.StatusInternalServerError)
		return
	}
	raw, err := conn.OpenStream(r.Context(), ep.ID, store.KindHTTP, withClientIP(nil, requestClientIP(r, i.trusted)))
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
	flow, untrack, err := i.trackUpgrade(r, stream, ep, outPath)
	defer untrack()
	if errors.Is(err, store.ErrNotFound) {
		writeIdentityUnauthorized(w)
		i.recordCode(http.StatusUnauthorized)
		return
	}
	if err != nil {
		i.log.Error("upgrade session lookup failed", "endpoint_id", ep.ID, "err", err)
		writeIdentityUnavailable(w)
		i.recordCode(http.StatusServiceUnavailable)
		return
	}
	if flow != nil && !i.upgrades.Confirm(flow, flushEpochFrom(r)) {
		http.Error(w, "access changed while the upgrade was being set up", http.StatusForbidden)
		i.recordCode(http.StatusForbidden)
		return
	}

	resp, br, ok := i.upgradeHandshake(w, stream, upgradeRequest(r, ep, outPath, i.trusted), ep)
	if !ok {
		return
	}

	client, clientBuf, err := hj.Hijack()
	if err != nil {
		i.log.Warn("hijack failed", "endpoint_id", ep.ID, "err", err)
		return
	}
	defer client.Close()
	if flow != nil && !i.upgrades.Attach(flow, client) {
		return
	}
	if err := writeUpgradeHead(client, resp); err != nil {
		return
	}

	in, out := tunnel.Splice(&bufferedConn{Conn: client, r: clientBuf.Reader}, &bufferedConn{Conn: stream, r: br})
	accessFrom(r).markUpgraded(in, out)
}

func upgradeRequest(r *http.Request, ep *store.Endpoint, outPath string, trusted []*net.IPNet) *http.Request {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHopHeaders(out.Header)
	preserveUpgradeHeaders(out.Header, r.Header)
	rewriteOutbound(r, out, ep, outPath, trusted)
	return out
}

func (i *Ingress) upgradeHandshake(w http.ResponseWriter, stream net.Conn, outReq *http.Request, ep *store.Endpoint) (*http.Response, *bufio.Reader, bool) {
	if err := outReq.Write(stream); err != nil {
		i.failUpgrade(w, "")
		return nil, nil, false
	}
	br := bufio.NewReader(stream)
	if reason, failed := tunnel.ReadStreamError(br); failed {
		i.failUpgrade(w, reason)
		return nil, nil, false
	}
	resp, err := readUpgradeResponse(br, outReq)
	if err != nil {
		i.failUpgrade(w, "")
		return nil, nil, false
	}
	sanitizeUpstreamResponse(resp.Header)
	applyResponsePolicy(resp.Header, ep)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		i.relayRefusedUpgrade(w, resp)
		return nil, nil, false
	}
	return resp, br, true
}

func (i *Ingress) failUpgrade(w http.ResponseWriter, reason string) {
	http.Error(w, upstreamUnreachableMessage(reason), http.StatusBadGateway)
	i.recordCode(http.StatusBadGateway)
}

func (i *Ingress) relayRefusedUpgrade(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	i.recordCode(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func readUpgradeResponse(br *bufio.Reader, req *http.Request) (*http.Response, error) {
	for {
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusSwitchingProtocols || resp.StatusCode < 100 || resp.StatusCode >= 200 {
			return resp, nil
		}
	}
}

func writeUpgradeHead(w io.Writer, resp *http.Response) error {
	bw := bufio.NewWriter(w)
	if _, err := fmt.Fprintf(bw, "HTTP/1.1 %s\r\n", resp.Status); err != nil {
		return err
	}
	if err := resp.Header.Write(bw); err != nil {
		return err
	}
	if _, err := bw.WriteString("\r\n"); err != nil {
		return err
	}
	return bw.Flush()
}

func newUpgradeTracker(i *Ingress, ids *identity.Service) *flows.Tracker {
	if ids == nil {
		return flows.NewTracker(i.log, "ingress upgrade", nil)
	}
	return flows.NewTracker(i.log, "ingress upgrade", ids)
}

type flushEpochKey struct{}

func flushEpochFrom(r *http.Request) uint64 {
	epoch, _ := r.Context().Value(flushEpochKey{}).(uint64)
	return epoch
}

func (i *Ingress) trackUpgrade(r *http.Request, stream net.Conn, ep *store.Endpoint, outPath string) (*flows.Flow, func(), error) {
	var principal *identity.Principal
	if id := identityFrom(r); id != nil {
		principal = id.principal
	}
	sessionID := ""
	if principal != nil {
		sessionID = principal.SessionID
	}
	flow, untrack := i.upgrades.Track(ep.OrgID, sessionID, stream, i.upgradeRecheck(r, ep.ID, outPath, principal))
	if sessionID == "" {
		return flow, untrack, nil
	}
	if _, err := i.identity.Sessions().GetTokenSession(r.Context(), sessionID); err != nil {
		return nil, untrack, err
	}
	return flow, untrack, nil
}

func (i *Ingress) upgradeRecheck(r *http.Request, endpointID, outPath string, principal *identity.Principal) flows.Recheck {
	if i.data == nil {
		return nil
	}
	return func(ctx context.Context) error {
		ep, err := i.data.GetEndpoint(ctx, endpointID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("endpoint %s: %w", endpointID, err)
		case err != nil:
			return fmt.Errorf("endpoint %s: %w: %w", endpointID, flows.ErrTransient, err)
		}
		if ep.Policy == nil || ep.Policy.Identity == nil {
			return nil
		}
		if i.deciders == nil {
			return errors.New("endpoint is identity gated but no decider is configured")
		}
		decision, err := i.decideEndpoint(r.WithContext(ctx), ep, outPath, principal)
		switch {
		case err != nil || decision.Reason == authz.ReasonUnavailable:
			return fmt.Errorf("endpoint %s decision: %w: %v", endpointID, flows.ErrTransient, err)
		case !decision.Allowed:
			return fmt.Errorf("endpoint access re-evaluated to %s", decision.Reason)
		}
		return nil
	}
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
		return strings.TrimSuffix(strings.ToLower(h), ".")
	}
	return strings.TrimSuffix(strings.ToLower(hostport), ".")
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
