package proxy

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var (
	alwaysBlockedNets = mustCIDRs(
		"0.0.0.0/8",
		"169.254.0.0/16",
		"224.0.0.0/4",
		"255.255.255.255/32",
		"fe80::/10",
		"ff00::/8",
		"fd00:ec2::254/128",
	)
	privateNets = mustCIDRs(
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"198.18.0.0/15",
		"240.0.0.0/4",
		"fc00::/7",
		"64:ff9b::/96",
		"2001::/32",
		"2002::/16",
	)
	internalNameSuffixes = []string{".localhost", ".svc", ".cluster.local", ".local", ".internal"}
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

type Guard struct {
	allowLoopback bool
	allowPrivate  bool
	allowed       []*net.IPNet
}

func NewGuard(allowLoopback, allowPrivate bool, allowedCIDRs []string) (*Guard, error) {
	g := &Guard{allowLoopback: allowLoopback, allowPrivate: allowPrivate}
	for _, c := range allowedCIDRs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("proxy: invalid allowed cidr %q: %w", c, err)
		}
		g.allowed = append(g.allowed, n)
	}
	return g, nil
}

func DefaultGuard() *Guard {
	return &Guard{}
}

func inAny(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func normalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func (g *Guard) IPAllowed(ip net.IP) bool {
	ip = normalizeIP(ip)
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || inAny(alwaysBlockedNets, ip) {
		return false
	}
	if ip.IsLoopback() {
		return g.allowLoopback
	}
	if ip.IsPrivate() || inAny(privateNets, ip) {
		return g.allowPrivate || inAny(g.allowed, ip)
	}
	return true
}

func (g *Guard) nameAllowed(host string) bool {
	if host == "localhost" && g.allowLoopback {
		return true
	}
	if g.allowPrivate || len(g.allowed) > 0 {
		return host != "localhost" || g.allowLoopback
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range internalNameSuffixes {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

func splitTarget(target string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(target)
	if err != nil {
		return "", "", fmt.Errorf("proxy: invalid target %q: %w", target, err)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return "", "", fmt.Errorf("proxy: invalid port in target %q", target)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", "", fmt.Errorf("proxy: invalid target %q: empty host", target)
	}
	return host, port, nil
}

func (g *Guard) ValidateTarget(target string) error {
	host, _, err := splitTarget(target)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !g.IPAllowed(ip) {
			return fmt.Errorf("proxy: target %q is a blocked address", target)
		}
		return nil
	}
	if !g.nameAllowed(host) {
		return fmt.Errorf("proxy: target %q is an internal name", target)
	}
	return nil
}

func (g *Guard) Resolve(ctx context.Context, target string) (string, error) {
	host, port, err := splitTarget(target)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !g.IPAllowed(ip) {
			return "", fmt.Errorf("proxy: target %q resolves to a blocked address", target)
		}
		return net.JoinHostPort(normalizeIP(ip).String(), port), nil
	}
	if !g.nameAllowed(host) {
		return "", fmt.Errorf("proxy: target %q is an internal name", target)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("proxy: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("proxy: target %q did not resolve", target)
	}
	for _, a := range addrs {
		if !g.IPAllowed(a.IP) {
			return "", fmt.Errorf("proxy: target %q resolves to a blocked address", target)
		}
	}
	return net.JoinHostPort(normalizeIP(addrs[0].IP).String(), port), nil
}
