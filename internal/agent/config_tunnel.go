package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"

	"github.com/mishmesh/mishmesh/internal/store"
)

type connectConfig struct {
	Endpoint    string `yaml:"endpoint"`
	Proto       string `yaml:"proto"`
	Listen      string `yaml:"listen"`
	IdleTimeout string `yaml:"idle_timeout"`
}

func (p *configParser) parseConnects(n *yaml.Node) []ConnectSpec {
	listens := make(map[string]string)
	var out []ConnectSpec
	p.eachNamed(n, "", "connect", connectKeys, func(name string, key, val *yaml.Node) {
		fail := func(format string, args ...any) {
			p.addf(key.Line, "connect %q: %s", name, fmt.Sprintf(format, args...))
		}
		var cc connectConfig
		if err := val.Decode(&cc); err != nil {
			fail("%s", yamlProblem(err))
			return
		}
		spec := ConnectSpec{Name: name, Endpoint: cc.Endpoint, Kind: cc.Proto}
		if spec.Endpoint == "" {
			spec.Endpoint = name
		}
		if !store.PortKind(spec.Kind) {
			fail("proto must be tcp or udp, got %q", cc.Proto)
		}
		if strings.TrimSpace(cc.Listen) == "" {
			fail("listen is required (a port like 51820 or host:port)")
		} else {
			spec.Listen = normalizeListen(cc.Listen)
			if _, _, err := net.SplitHostPort(spec.Listen); err != nil {
				fail("listen %q must be a port or host:port", cc.Listen)
			}
			owner := spec.Kind + " " + spec.Listen
			if other, dup := listens[owner]; dup {
				fail("listen %s is already used by connect %q", spec.Listen, other)
			}
			listens[owner] = name
		}
		if cc.IdleTimeout != "" {
			d, err := time.ParseDuration(cc.IdleTimeout)
			if err != nil || d <= 0 {
				fail("idle_timeout %q must be a positive duration like 90s", cc.IdleTimeout)
			}
			if cc.Proto != store.KindUDP {
				fail("idle_timeout only applies to udp connects")
			}
			spec.Idle = d
		}
		out = append(out, spec)
	})
	return out
}

func (p *configParser) parseTunnels(n *yaml.Node) []NamedTunnel {
	var out []NamedTunnel
	p.eachNamed(n, "", "tunnel", tunnelKeys, func(name string, key, val *yaml.Node) {
		var tc TunnelConfig
		if err := val.Decode(&tc); err != nil {
			p.addf(val.Line, "tunnel %q: %s", name, yamlProblem(err))
			return
		}
		nt := NamedTunnel{Name: name, TunnelConfig: tc, line: key.Line}
		p.validateTunnel(nt)
		out = append(out, nt)
	})
	return out
}

func (p *configParser) validateTunnel(t NamedTunnel) {
	fail := func(format string, args ...any) {
		p.addf(t.line, "tunnel %q: %s", t.Name, fmt.Sprintf(format, args...))
	}
	if !slices.Contains(validProtos, t.Proto) {
		fail("proto must be one of %s, got %q", validProtoMsg, t.Proto)
		return
	}
	if strings.TrimSpace(t.Addr) == "" {
		fail("addr is required (a port like 3000 or host:port)")
	}
	if t.Port < 0 || t.Port > 65535 {
		fail("port %d is out of range", t.Port)
	}
	if t.Subdomain != "" && t.Domain != "" {
		fail("subdomain and domain are mutually exclusive")
	}
	switch t.Proto {
	case store.KindTCP, store.KindUDP:
		if t.Subdomain != "" || t.Domain != "" {
			fail("subdomain and domain do not apply to %s tunnels (use port)", t.Proto)
		}
		if t.TargetHTTPS {
			fail("target_https does not apply to %s tunnels", t.Proto)
		}
		if t.Addr != "" && !strings.Contains(strings.TrimPrefix(t.Addr, t.Proto+"://"), ":") && !isDigits(t.Addr) {
			fail("addr %q must be a port or host:port", t.Addr)
		}
		if t.Private && t.Port != 0 {
			fail("private tunnels have no public port; remove port")
		}
	case store.KindHTTP, store.KindTLS:
		if t.Port != 0 {
			fail("port only applies to tcp and udp tunnels")
		}
		if t.Private {
			fail("private only applies to tcp and udp tunnels")
		}
		if t.Proto == store.KindTLS && t.TargetHTTPS {
			fail("target_https does not apply to tls tunnels: TLS passthrough forwards the encrypted stream untouched")
		}
		if t.Proto == store.KindTLS && t.Subdomain == "" && t.Domain == "" {
			fail("tls tunnels need a subdomain or domain to route by SNI")
		}
	}
	if t.Insecure && t.Proto != store.KindHTTP {
		fail("insecure only applies to http tunnels")
	}
	if len(t.Policy) > 0 {
		if _, err := encodePolicy(t.Policy, skipPasswordHash); err != nil {
			fail("policy: %v", err)
		}
	}
}

type passwordHasher func(password string) (string, error)

func skipPasswordHash(string) (string, error) {
	return "", nil
}

func bcryptPasswordHash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func hashBasicAuthPassword(m map[string]any, hasher passwordHasher) (map[string]any, error) {
	v, ok := m["basic_auth_password"]
	if !ok {
		return m, nil
	}
	password, isString := v.(string)
	if !isString || password == "" {
		return nil, errors.New("basic_auth_password must be a non-empty string")
	}
	if _, dup := m["basic_auth_hash"]; dup {
		return nil, errors.New("basic_auth_password and basic_auth_hash are mutually exclusive")
	}
	hash, err := hasher(password)
	if err != nil {
		return nil, fmt.Errorf("hash basic_auth_password: %w", err)
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if k != "basic_auth_password" {
			out[k] = val
		}
	}
	out["basic_auth_hash"] = hash
	return out, nil
}

func encodePolicy(m map[string]any, hasher passwordHasher) (json.RawMessage, error) {
	m, err := hashBasicAuthPassword(m, hasher)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var pol store.EndpointPolicy
	if err := dec.Decode(&pol); err != nil {
		return nil, err
	}
	if pol.RateLimit != nil {
		if err := pol.RateLimit.Validate(); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func (c *FileConfig) TunnelNames() []string {
	names := make([]string, len(c.Tunnels))
	for i, t := range c.Tunnels {
		names[i] = t.Name
	}
	return names
}

func (c *FileConfig) Specs(selected []string) ([]EndpointSpec, error) {
	byName := make(map[string]NamedTunnel, len(c.Tunnels))
	for _, t := range c.Tunnels {
		byName[t.Name] = t
	}
	tunnels := c.Tunnels
	if len(selected) > 0 {
		tunnels = tunnels[:0:0]
		seen := make(map[string]bool)
		for _, name := range selected {
			t, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("unknown tunnel %q (configured: %s)", name, strings.Join(c.TunnelNames(), ", "))
			}
			if !seen[name] {
				seen[name] = true
				tunnels = append(tunnels, t)
			}
		}
	}
	specs := make([]EndpointSpec, 0, len(tunnels))
	for _, t := range tunnels {
		spec, err := t.Spec()
		if err != nil {
			return nil, fmt.Errorf("tunnel %q: %w", t.Name, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func (t NamedTunnel) Spec() (EndpointSpec, error) {
	addr, schemeTLS := NormalizeTarget(t.Addr)
	spec := EndpointSpec{
		Name:           t.Name,
		Kind:           t.Proto,
		Lifecycle:      store.LifecycleEphemeral,
		Subdomain:      t.Subdomain,
		Domain:         t.Domain,
		Port:           t.Port,
		Private:        t.Private,
		LocalTarget:    addr,
		TargetTLS:      (t.TargetHTTPS || schemeTLS) && t.Proto == store.KindHTTP,
		TargetInsecure: t.Insecure,
	}
	if t.Reserved || t.Subdomain != "" || t.Domain != "" || t.Port != 0 {
		spec.Lifecycle = store.LifecycleReserved
	}
	if len(t.Policy) > 0 {
		raw, err := encodePolicy(t.Policy, bcryptPasswordHash)
		if err != nil {
			return EndpointSpec{}, fmt.Errorf("policy: %w", err)
		}
		spec.Policy = raw
	}
	return spec, nil
}

func NormalizeTarget(arg string) (addr string, useTLS bool) {
	switch {
	case strings.HasPrefix(arg, "https://"):
		arg = strings.TrimPrefix(arg, "https://")
		useTLS = true
	case strings.HasPrefix(arg, "tls://"):
		arg = strings.TrimPrefix(arg, "tls://")
		useTLS = true
	case strings.HasPrefix(arg, "http://"):
		arg = strings.TrimPrefix(arg, "http://")
	case strings.HasPrefix(arg, "tcp://"):
		arg = strings.TrimPrefix(arg, "tcp://")
	case strings.HasPrefix(arg, "udp://"):
		arg = strings.TrimPrefix(arg, "udp://")
	}
	arg = strings.TrimSuffix(arg, "/")
	if strings.HasPrefix(arg, ":") {
		return "127.0.0.1" + arg, useTLS
	}
	if !strings.Contains(arg, ":") {
		return "127.0.0.1:" + arg, useTLS
	}
	return arg, useTLS
}
