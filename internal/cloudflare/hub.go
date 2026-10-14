package cloudflare

import "net"

func OriginForListenAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
