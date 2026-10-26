package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

var ErrBlocked = errors.New("proxy: blocked target")

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
		"64:ff9b:1::/48",
		"fec0::/10",
		"2001::/32",
		"2002::/16",
	)
	embeddedIPv4Nets     = mustCIDRs("64:ff9b::/96", "::/96")
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
	lookup        func(ctx context.Context, host string) ([]net.IPAddr, error)
	tlsConfigs    sync.Map
	tlsConfigN    atomic.Int64
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
	if v4 := embeddedIPv4(ip); v4 != nil && !g.IPAllowed(v4) {
		return false
	}
	if ip.IsPrivate() || inAny(privateNets, ip) {
		return g.allowPrivate || inAny(g.allowed, ip)
	}
	return true
}

func embeddedIPv4(ip net.IP) net.IP {
	if len(ip) != net.IPv6len || !inAny(embeddedIPv4Nets, ip) {
		return nil
	}
	return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
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
	t, err := ParseTarget(target)
	if err != nil {
		return "", "", err
	}
	return t.Host, t.Port, nil
}

func (g *Guard) ValidateTarget(target string) error {
	host, _, err := splitTarget(target)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !g.IPAllowed(ip) {
			return fmt.Errorf("%w: target %q is a blocked address", ErrBlocked, target)
		}
		return nil
	}
	if !g.nameAllowed(host) {
		return fmt.Errorf("%w: target %q is an internal name", ErrBlocked, target)
	}
	return nil
}

const maxResolvedAddrs = 8

func (g *Guard) Resolve(ctx context.Context, target string) (string, error) {
	addrs, err := g.ResolveAll(ctx, target)
	if err != nil {
		return "", err
	}
	return addrs[0], nil
}

func (g *Guard) ResolveAll(ctx context.Context, target string) ([]string, error) {
	host, port, err := splitTarget(target)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !g.IPAllowed(ip) {
			return nil, fmt.Errorf("%w: target %q resolves to a blocked address", ErrBlocked, target)
		}
		return []string{net.JoinHostPort(normalizeIP(ip).String(), port)}, nil
	}
	if !g.nameAllowed(host) {
		return nil, fmt.Errorf("%w: target %q is an internal name", ErrBlocked, target)
	}
	lookup := g.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("proxy: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("proxy: target %q did not resolve", target)
	}
	out := make([]string, 0, min(len(addrs), maxResolvedAddrs))
	for _, a := range addrs {
		if !g.IPAllowed(a.IP) {
			return nil, fmt.Errorf("%w: target %q resolves to a blocked address", ErrBlocked, target)
		}
		addr := net.JoinHostPort(normalizeIP(a.IP).String(), port)
		if len(out) < maxResolvedAddrs && !slices.Contains(out, addr) {
			out = append(out, addr)
		}
	}
	return out, nil
}

func (g *Guard) WithResolver(r *net.Resolver) *Guard {
	g.lookup = r.LookupIPAddr
	return g
}
