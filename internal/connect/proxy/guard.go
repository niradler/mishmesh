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

	"github.com/mishmesh/mishmesh/internal/netblock"
)

var ErrBlocked = errors.New("proxy: blocked target")

var internalNameSuffixes = []string{".localhost", ".svc", ".cluster.local", ".local", ".internal"}

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

func (g *Guard) IPAllowed(ip net.IP) bool {
	return netblock.Policy{AllowLoopback: g.allowLoopback, AllowPrivate: g.allowPrivate, AllowedCIDRs: g.allowed}.IPAllowed(ip)
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

func (g *Guard) ResolveAll(ctx context.Context, target string) ([]string, error) {
	host, port, err := splitTarget(target)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !g.IPAllowed(ip) {
			return nil, fmt.Errorf("%w: target %q resolves to a blocked address", ErrBlocked, target)
		}
		return []string{net.JoinHostPort(netblock.Normalize(ip).String(), port)}, nil
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
		addr := net.JoinHostPort(netblock.Normalize(a.IP).String(), port)
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
