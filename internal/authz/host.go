package authz

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

func NormalizeHost(host string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if addr, err := netip.ParseAddr(h); err == nil {
		if addr.Zone() != "" {
			return "", invalid("host %q must not carry a zone", truncate(host))
		}
		return addr.Unmap().String(), nil
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > maxHostLength {
		return "", invalid("host must be 1-%d characters", maxHostLength)
	}
	if strings.ContainsAny(h, "\x00\\/@:[]% \t\r\n") || strings.Contains(h, "..") || strings.HasPrefix(h, ".") {
		return "", invalid("host %q is not a valid name", truncate(host))
	}
	return h, nil
}

func NormalizeDestination(hostport string) (string, error) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(hostport))
	if err != nil {
		return "", invalid("destination %q must be host:port", truncate(hostport))
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portStr {
		return "", invalid("destination %q has an invalid port", truncate(hostport))
	}
	h, err := NormalizeHost(host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(h, portStr), nil
}
