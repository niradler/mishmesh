package agent

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/mishmesh/mishmesh/internal/netblock"
)

type allowRule struct {
	cidr    *net.IPNet
	host    string
	ports   map[int]bool
	anyPort bool
}

type Allowlist struct {
	rules    []allowRule
	lookupIP func(ctx context.Context, host string) ([]net.IPAddr, error)
}

func NewAllowlist(specs []string) *Allowlist {
	a := &Allowlist{lookupIP: net.DefaultResolver.LookupIPAddr}
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		a.rules = append(a.rules, parseRule(spec))
	}
	return a
}

func parseRule(spec string) allowRule {
	hostPart := spec
	var ports map[int]bool
	anyPort := true
	if idx := strings.LastIndex(spec, ":"); idx != -1 && !strings.Contains(spec[idx+1:], "/") {
		maybePorts := spec[idx+1:]
		if isPortList(maybePorts) {
			hostPart = spec[:idx]
			ports = make(map[int]bool)
			anyPort = false
			for _, p := range strings.Split(maybePorts, ";") {
				if n, err := strconv.Atoi(p); err == nil {
					ports[n] = true
				}
			}
		}
	}
	rule := allowRule{ports: ports, anyPort: anyPort}
	if _, network, err := net.ParseCIDR(hostPart); err == nil {
		rule.cidr = network
	} else {
		rule.host = strings.ToLower(hostPart)
	}
	return rule
}

func isPortList(s string) bool {
	if s == "" {
		return false
	}
	for _, p := range strings.Split(s, ";") {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

func (a *Allowlist) Resolve(ctx context.Context, target string) (dialAddr, serverName string, ok bool) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return "", "", false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", "", false
	}
	if net.ParseIP(host) == nil && !a.couldAdmitHost(host, port) {
		return "", "", false
	}
	ips := a.resolveIPs(ctx, host)
	if len(ips) == 0 {
		return "", "", false
	}
	for _, ip := range ips {
		if hardDenied(ip) {
			return "", "", false
		}
	}
	for _, ip := range ips {
		if a.allowsIP(ip, host, port) {
			return net.JoinHostPort(ip.String(), portStr), host, true
		}
	}
	return "", "", false
}

func (a *Allowlist) couldAdmitHost(host string, port int) bool {
	host = strings.ToLower(host)
	for _, rule := range a.rules {
		if !rule.anyPort && !rule.ports[port] {
			continue
		}
		if rule.cidr != nil || rule.host == host {
			return true
		}
	}
	return false
}

func (a *Allowlist) allowsIP(ip net.IP, host string, port int) bool {
	for _, rule := range a.rules {
		if !rule.anyPort && !rule.ports[port] {
			continue
		}
		if rule.cidr != nil {
			if rule.cidr.Contains(ip) {
				return true
			}
			continue
		}
		if rule.host != "" && rule.host == strings.ToLower(host) {
			return true
		}
	}
	return false
}

func (a *Allowlist) resolveIPs(ctx context.Context, host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	addrs, err := a.lookupIP(ctx, host)
	if err != nil {
		return nil
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	return ips
}

var hardDenyPolicy = netblock.Policy{AllowPrivate: true}

func hardDenied(ip net.IP) bool {
	return !hardDenyPolicy.IPAllowed(ip)
}
