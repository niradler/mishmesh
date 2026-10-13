package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Target struct {
	Scheme string
	Host   string
	Port   string
}

const SchemeUDP = "udp"

func (t Target) TLS() bool { return t.Scheme == "https" }

func (t Target) Datagram() bool { return t.Scheme == SchemeUDP }

func (t Target) Addr() string { return net.JoinHostPort(t.Host, t.Port) }

func ParseTarget(s string) (Target, error) {
	scheme := "http"
	hostPort := s
	if i := strings.Index(s, "://"); i >= 0 {
		scheme = strings.ToLower(s[:i])
		if scheme != "http" && scheme != "https" && scheme != SchemeUDP {
			return Target{}, fmt.Errorf("proxy: invalid target %q: unsupported scheme %q", s, scheme)
		}
		u, err := url.Parse(s)
		if err != nil {
			return Target{}, fmt.Errorf("proxy: invalid target %q: %w", s, err)
		}
		if u.User != nil {
			return Target{}, fmt.Errorf("proxy: invalid target %q: userinfo not allowed", s)
		}
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(s[i+3:], "/?#") {
			return Target{}, fmt.Errorf("proxy: invalid target %q: path or query not allowed", s)
		}
		hostPort = u.Host
	} else if strings.ContainsAny(s, "/?#@") {
		return Target{}, fmt.Errorf("proxy: invalid target %q: must be host:port", s)
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return Target{}, fmt.Errorf("proxy: invalid target %q: %w", s, err)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return Target{}, fmt.Errorf("proxy: invalid port in target %q", s)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return Target{}, fmt.Errorf("proxy: invalid target %q: empty host", s)
	}
	return Target{Scheme: scheme, Host: host, Port: port}, nil
}
