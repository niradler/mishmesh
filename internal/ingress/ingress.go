package ingress

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

type Options struct {
	Data       store.DataStore
	Conns      store.ConnectionStore
	Log        *slog.Logger
	BaseDomain string
}

type Ingress struct {
	data     store.DataStore
	conns    store.ConnectionStore
	log      *slog.Logger
	apexHost string
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
	}
}

func (i *Ingress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	endpointID, outPath, ok := i.resolve(r)
	if !ok {
		http.Error(w, "tunnel not found", http.StatusNotFound)
		return
	}
	conn, ok := i.conns.ResolveEndpoint(endpointID)
	if !ok {
		http.Error(w, "tunnel offline", http.StatusBadGateway)
		return
	}
	i.proxyHTTP(w, r, conn, endpointID, outPath)
}

func (i *Ingress) resolve(r *http.Request) (endpointID, outPath string, ok bool) {
	if id, rest, ok := pathEndpoint(r.URL.Path); ok {
		return id, rest, true
	}
	if sub, ok := i.subdomain(r.Host); ok {
		ep, err := i.data.GetEndpointBySubdomain(r.Context(), sub)
		if err != nil {
			return "", "", false
		}
		return ep.ID, r.URL.Path, true
	}
	return "", "", false
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

func (i *Ingress) proxyHTTP(w http.ResponseWriter, r *http.Request, conn store.AgentConn, endpointID, outPath string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream, err := conn.OpenStream(ctx, endpointID, store.KindHTTP, nil)
	if err != nil {
		http.Error(w, "tunnel stream failed", http.StatusBadGateway)
		i.log.Warn("open stream failed", "endpoint_id", endpointID, "err", err)
		return
	}
	defer stream.Close()

	outReq := r.Clone(ctx)
	outReq.RequestURI = ""
	outReq.URL.Path = outPath
	outReq.URL.RawPath = ""
	stripHopHeaders(outReq.Header)

	if err := outReq.Write(stream); err != nil {
		http.Error(w, "tunnel write failed", http.StatusBadGateway)
		return
	}

	resp, err := readResponse(stream, outReq)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			http.Error(w, "tunnel read failed", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()

	stripHopHeaders(resp.Header)
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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

func readResponse(stream net.Conn, req *http.Request) (*http.Response, error) {
	return http.ReadResponse(bufio.NewReader(stream), req)
}
