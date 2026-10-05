package clientip

import (
	"net"
	"net/http"
	"strings"
)

func Resolve(r *http.Request, trusted []*net.IPNet) net.IP {
	peer := parseHost(r.RemoteAddr)
	if peer == nil || !contains(trusted, peer) {
		return peer
	}
	hops := forwardedChain(r.Header.Values("X-Forwarded-For"))
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(hops[i])
		if ip == nil {
			return peer
		}
		if !contains(trusted, ip) {
			return ip
		}
	}
	if len(hops) > 0 {
		if ip := net.ParseIP(hops[0]); ip != nil {
			return ip
		}
	}
	return peer
}

func forwardedChain(values []string) []string {
	var hops []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	return hops
}

func parseHost(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return net.ParseIP(host)
}

func contains(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
