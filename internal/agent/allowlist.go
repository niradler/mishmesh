package agent

import (
	"net"
	"strconv"
	"strings"
)

type allowRule struct {
	cidr    *net.IPNet
	host    string
	ports   map[int]bool
	anyPort bool
}

type Allowlist struct {
	rules []allowRule
}

func NewAllowlist(specs []string) *Allowlist {
	a := &Allowlist{}
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

func (a *Allowlist) Allowed(target string) bool {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return false
	}
	ips := resolveIPs(host)
	if len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if hardDenied(ip) {
			return false
		}
	}
	for _, rule := range a.rules {
		if !rule.anyPort && !rule.ports[port] {
			continue
		}
		if rule.cidr != nil {
			for _, ip := range ips {
				if rule.cidr.Contains(ip) {
					return true
				}
			}
			continue
		}
		if rule.host != "" && rule.host == strings.ToLower(host) {
			return true
		}
	}
	return false
}

func resolveIPs(host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	return addrs
}

func hardDenied(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip.Equal(net.ParseIP("169.254.169.254")) || ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return true
	}
	return false
}
