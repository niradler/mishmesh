package ingress

import (
	"maps"
	"net"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/clientip"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func withClientIP(meta map[string]string, ip string) map[string]string {
	if ip == "" {
		return meta
	}
	out := make(map[string]string, len(meta)+1)
	maps.Copy(out, meta)
	out[tunnel.MetaClientIP] = ip
	return out
}

func peerIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return ""
	}
	return host
}

func requestClientIP(r *http.Request, trusted []*net.IPNet) string {
	ip := clientip.Resolve(r, trusted)
	if ip == nil {
		return ""
	}
	return ip.String()
}
