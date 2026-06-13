package ingress

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

type Meter interface {
	AddBytes(kind string, in, out int64)
	HTTPRequest(code int)
}

type Options struct {
	Data       store.DataStore
	Conns      store.ConnectionStore
	Log        *slog.Logger
	BaseDomain string
	Meter      Meter
}

type Ingress struct {
	data     store.DataStore
	conns    store.ConnectionStore
	log      *slog.Logger
	apexHost string
	meter    Meter
}

func New(opts Options) *Ingress {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Ingress{
		data:     opts.Data,
		conns:    opts.Conns,
		log:      log,
		apexHost: hostOnly(opts.BaseDomain),
		meter:    opts.Meter,
	}
}

func (i *Ingress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ep, outPath, ok := i.resolve(r)
	if !ok {
		http.Error(w, "tunnel not found", http.StatusNotFound)
		return
	}
	if !applyPolicyGate(w, r, ep) {
		return
	}
	conn, ok := i.conns.ResolveEndpoint(ep.ID)
	if !ok {
		http.Error(w, "tunnel offline", http.StatusBadGateway)
		i.recordCode(http.StatusBadGateway)
		return
	}
	if isUpgrade(r) {
		i.proxyUpgrade(w, r, conn, ep, outPath)
		return
	}
	i.proxyHTTP(w, r, conn, ep, outPath)
}

func (i *Ingress) resolve(r *http.Request) (ep *store.Endpoint, outPath string, ok bool) {
	if id, rest, isPath := pathEndpoint(r.URL.Path); isPath {
		e, err := i.data.GetEndpoint(r.Context(), id)
		if err != nil {
			return nil, "", false
		}
		return e, rest, true
	}
	host := hostOnly(r.Host)
	if sub, isSub := i.subdomain(host); isSub {
		e, err := i.data.GetEndpointBySubdomain(r.Context(), sub)
		if err != nil {
			return nil, "", false
		}
		return e, r.URL.Path, true
	}
	if host != "" && host != i.apexHost {
		if e, err := i.data.GetEndpointByDomain(r.Context(), host); err == nil {
			return e, r.URL.Path, true
		}
	}
	return nil, "", false
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

func (i *Ingress) proxyHTTP(w http.ResponseWriter, r *http.Request, conn store.AgentConn, ep *store.Endpoint, outPath string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream, err := conn.OpenStream(ctx, ep.ID, store.KindHTTP, nil)
	if err != nil {
		http.Error(w, "tunnel stream failed", http.StatusBadGateway)
		i.recordCode(http.StatusBadGateway)
		i.log.Warn("open stream failed", "endpoint_id", ep.ID, "err", err)
		return
	}
	defer stream.Close()

	outReq := buildOutboundRequest(r, ctx, ep, outPath)
	stripHopHeaders(outReq.Header)
	applyRequestPolicy(outReq, ep)

	counted := &countWriter{w: stream}
	if err := outReq.Write(counted); err != nil {
		http.Error(w, "tunnel write failed", http.StatusBadGateway)
		i.recordCode(http.StatusBadGateway)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), outReq)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			http.Error(w, "tunnel read failed", http.StatusBadGateway)
			i.recordCode(http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()

	stripHopHeaders(resp.Header)
	applyResponsePolicy(resp.Header, ep)
	gz := shouldCompress(ep, r, resp)
	if gz {
		resp.Header.Del("Content-Length")
		resp.Header.Set("Content-Encoding", "gzip")
		resp.Header.Add("Vary", "Accept-Encoding")
	}
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	var dst io.Writer = w
	if gz {
		zw := gzip.NewWriter(w)
		defer zw.Close()
		dst = zw
	}
	n, _ := io.Copy(dst, resp.Body)
	i.meterBytes(ep, store.KindHTTP, counted.n, n)
	i.recordCode(resp.StatusCode)
}

func (i *Ingress) proxyUpgrade(w http.ResponseWriter, r *http.Request, conn store.AgentConn, ep *store.Endpoint, outPath string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "upgrade unsupported", http.StatusInternalServerError)
		return
	}
	stream, err := conn.OpenStream(r.Context(), ep.ID, store.KindHTTP, nil)
	if err != nil {
		http.Error(w, "tunnel stream failed", http.StatusBadGateway)
		i.recordCode(http.StatusBadGateway)
		return
	}
	defer stream.Close()

	outReq := buildOutboundRequest(r, r.Context(), ep, outPath)
	stripHopHeaders(outReq.Header)
	preserveUpgradeHeaders(outReq.Header, r.Header)
	applyRequestPolicy(outReq, ep)
	if err := outReq.Write(stream); err != nil {
		http.Error(w, "tunnel write failed", http.StatusBadGateway)
		i.recordCode(http.StatusBadGateway)
		return
	}

	client, clientBuf, err := hj.Hijack()
	if err != nil {
		i.log.Warn("hijack failed", "endpoint_id", ep.ID, "err", err)
		return
	}
	defer client.Close()

	errc := make(chan error, 2)
	var up, down int64
	go func() { n, e := io.Copy(stream, clientBuf); up = n; errc <- e }()
	go func() { n, e := io.Copy(client, stream); down = n; errc <- e }()
	<-errc
	i.meterBytes(ep, store.KindHTTP, up, down)
}

func buildOutboundRequest(r *http.Request, ctx context.Context, ep *store.Endpoint, outPath string) *http.Request {
	outReq := r.Clone(ctx)
	outReq.RequestURI = ""
	if ep.Policy != nil && ep.Policy.StripPathPrefix != "" {
		outPath = strings.TrimPrefix(outPath, ep.Policy.StripPathPrefix)
		if !strings.HasPrefix(outPath, "/") {
			outPath = "/" + outPath
		}
	}
	if ep.Policy != nil && ep.Policy.AddPathPrefix != "" {
		outPath = strings.TrimRight(ep.Policy.AddPathPrefix, "/") + outPath
	}
	outReq.URL.Path = outPath
	outReq.URL.RawPath = ""
	if ep.Policy != nil && ep.Policy.HostHeader != "" {
		outReq.Host = ep.Policy.HostHeader
		outReq.Header.Set("Host", ep.Policy.HostHeader)
	}
	return outReq
}

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

func (i *Ingress) meterBytes(ep *store.Endpoint, kind string, in, out int64) {
	if ep != nil && ep.OrgID != "" {
		i.conns.AddUsage(ep.OrgID, in+out)
	}
	if i.meter != nil {
		i.meter.AddBytes(kind, in, out)
	}
}

func (i *Ingress) recordCode(code int) {
	if i.meter != nil {
		i.meter.HTTPRequest(code)
	}
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
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
