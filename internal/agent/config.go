package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"

	"github.com/mishmesh/mishmesh/internal/store"
)

type FileConfig struct {
	Gateway        string
	GatewayHeaders http.Header
	Token          string
	Allow          []string
	LogLevel       string
	Tunnels        []NamedTunnel
	Connects       []ConnectSpec
	Egress         *EgressSpec
}

type NamedTunnel struct {
	Name string
	TunnelConfig
	line int
}

type TunnelConfig struct {
	Proto       string         `yaml:"proto"`
	Addr        string         `yaml:"addr"`
	Subdomain   string         `yaml:"subdomain"`
	Domain      string         `yaml:"domain"`
	Port        int            `yaml:"port"`
	Reserved    bool           `yaml:"reserved"`
	Private     bool           `yaml:"private"`
	TargetHTTPS bool           `yaml:"target_https"`
	Insecure    bool           `yaml:"insecure"`
	Policy      map[string]any `yaml:"policy"`
}

type ConfigError struct {
	Path     string
	Problems []string
}

func (e *ConfigError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid config %s:", e.Path)
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return b.String()
}

var (
	envRef          = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)
	tunnelName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	headerName      = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	reservedHeaders = []string{"Authorization", "Connection", "Content-Length", "Host", "Transfer-Encoding", "Upgrade", "X-Mishmesh-User"}
	rootKeys        = []string{"gateway", "gateway_headers", "token", "allow", "log_level", "tunnels", "connects", "egress"}
	tunnelKeys      = []string{"proto", "addr", "subdomain", "domain", "port", "reserved", "private", "target_https", "insecure", "policy"}
	connectKeys     = []string{"endpoint", "proto", "listen", "idle_timeout"}
	egressKeys      = []string{"listen", "socks_listen", "require_auth", "exit", "serve_exit", "exit_allow", "originate"}
	originateKeys   = []string{"listen", "upstream", "token"}
	validProtos     = []string{store.KindHTTP, store.KindTCP, store.KindTLS, store.KindUDP}
	validProtoMsg   = strings.Join(validProtos, ", ")
)

func DefaultConfigPaths() []string {
	paths := []string{"mishmesh.yml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "mishmesh", "agent.yml"))
	}
	return append(paths, "/etc/mishmesh/agent.yml")
}

func FindConfig(paths []string) (string, error) {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config file found (looked in %s); pass --config", strings.Join(paths, ", "))
}

func LoadConfigFile(path string, lookup func(string) (string, bool)) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return ParseConfig(path, data, lookup)
}

func ParseConfig(path string, data []byte, lookup func(string) (string, bool)) (*FileConfig, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return nil, &ConfigError{Path: path, Problems: []string{"cannot parse YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, &ConfigError{Path: path, Problems: []string{"top level must be a mapping with gateway, token, allow and tunnels"}}
	}
	p := &configParser{lookup: lookup}
	cfg := p.parseRoot(doc.Content[0])
	if len(p.problems) > 0 {
		return nil, &ConfigError{Path: path, Problems: p.problems}
	}
	return cfg, nil
}

type configParser struct {
	lookup   func(string) (string, bool)
	problems []string
}

func (p *configParser) addf(line int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if line > 0 {
		msg = fmt.Sprintf("line %d: %s", line, msg)
	}
	p.problems = append(p.problems, msg)
}

func (p *configParser) expand(n *yaml.Node) {
	switch n.Kind {
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "${") {
			return
		}
		n.Value = envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			sub := envRef.FindStringSubmatch(m)
			if v, ok := p.lookup(sub[1]); ok && v != "" {
				return v
			}
			if strings.HasPrefix(m, "${"+sub[1]+":-") {
				return sub[2]
			}
			p.addf(n.Line, "environment variable %s is not set (use ${%s:-default} to provide a fallback)", sub[1], sub[1])
			return ""
		})
		if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 {
			n.Tag = ""
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			p.expand(n.Content[i])
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			p.expand(c)
		}
	}
}

func (p *configParser) checkKeys(n *yaml.Node, allowed []string, where string) {
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i]
		if !slices.Contains(allowed, key.Value) {
			p.addf(key.Line, "unknown field %q in %s (allowed: %s)", key.Value, where, strings.Join(allowed, ", "))
		}
	}
}

func (p *configParser) parseRoot(root *yaml.Node) *FileConfig {
	p.checkKeys(root, rootKeys, "config")
	p.expand(root)
	var raw struct {
		Gateway        string            `yaml:"gateway"`
		GatewayHeaders map[string]string `yaml:"gateway_headers"`
		Token          string            `yaml:"token"`
		Allow          []string          `yaml:"allow"`
		LogLevel       string            `yaml:"log_level"`
		Tunnels        yaml.Node         `yaml:"tunnels"`
		Connects       yaml.Node         `yaml:"connects"`
		Egress         yaml.Node         `yaml:"egress"`
	}
	if err := root.Decode(&raw); err != nil {
		p.addf(root.Line, "%s", strings.ReplaceAll(strings.TrimPrefix(err.Error(), "yaml: "), "\n", "; "))
		return nil
	}
	cfg := &FileConfig{Gateway: raw.Gateway, Token: raw.Token, Allow: raw.Allow, LogLevel: raw.LogLevel}
	cfg.GatewayHeaders = p.parseGatewayHeaders(root.Line, raw.GatewayHeaders)
	if cfg.Gateway != "" {
		if _, err := NormalizeGatewayURL(cfg.Gateway); err != nil {
			p.addf(root.Line, "gateway: %v", err)
		}
	}
	for _, rule := range cfg.Allow {
		if strings.TrimSpace(rule) == "" {
			p.addf(root.Line, "allow: empty rule")
		}
	}
	switch {
	case raw.Tunnels.Kind == 0 || raw.Tunnels.Tag == "!!null":
	case raw.Tunnels.Kind != yaml.MappingNode:
		p.addf(raw.Tunnels.Line, "tunnels must be a mapping of name to tunnel definition")
	default:
		cfg.Tunnels = p.parseTunnels(&raw.Tunnels)
	}
	switch {
	case raw.Connects.Kind == 0 || raw.Connects.Tag == "!!null":
	case raw.Connects.Kind != yaml.MappingNode:
		p.addf(raw.Connects.Line, "connects must be a mapping of name to connect definition")
	default:
		cfg.Connects = p.parseConnects(&raw.Connects)
	}
	switch {
	case raw.Egress.Kind == 0 || raw.Egress.Tag == "!!null":
	case raw.Egress.Kind != yaml.MappingNode:
		p.addf(raw.Egress.Line, "egress must be a mapping")
	default:
		cfg.Egress = p.parseEgress(&raw.Egress, cfg.Connects)
	}
	return cfg
}

func (p *configParser) parseGatewayHeaders(line int, raw map[string]string) http.Header {
	if len(raw) == 0 {
		return nil
	}
	h := http.Header{}
	for name, value := range raw {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		switch {
		case !headerName.MatchString(canonical):
			p.addf(line, "gateway_headers: %q is not a valid header name", name)
		case slices.Contains(reservedHeaders, canonical) || strings.HasPrefix(canonical, "Sec-Websocket-"):
			p.addf(line, "gateway_headers: %s is set by the agent and cannot be overridden", canonical)
		case strings.TrimSpace(value) == "":
			p.addf(line, "gateway_headers: %s has an empty value", canonical)
		case strings.ContainsAny(value, "\r\n"):
			p.addf(line, "gateway_headers: %s value contains a line break", canonical)
		default:
			h.Set(canonical, strings.TrimSpace(value))
		}
	}
	return h
}

type egressConfig struct {
	Listen      string    `yaml:"listen"`
	SocksListen string    `yaml:"socks_listen"`
	RequireAuth bool      `yaml:"require_auth"`
	Exit        string    `yaml:"exit"`
	ServeExit   bool      `yaml:"serve_exit"`
	ExitAllow   []string  `yaml:"exit_allow"`
	Originate   yaml.Node `yaml:"originate"`
}

type originateConfig struct {
	Listen   string `yaml:"listen"`
	Upstream string `yaml:"upstream"`
	Token    string `yaml:"token"`
}

func (p *configParser) parseEgress(n *yaml.Node, connects []ConnectSpec) *EgressSpec {
	p.checkKeys(n, egressKeys, "egress")
	var ec egressConfig
	if err := n.Decode(&ec); err != nil {
		p.addf(n.Line, "egress: %s", strings.ReplaceAll(strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n  "), "\n  ", "; "))
		return nil
	}
	spec := &EgressSpec{RequireAuth: ec.RequireAuth, Exit: strings.TrimSpace(ec.Exit), ServeExit: ec.ServeExit}
	switch {
	case strings.TrimSpace(ec.Listen) != "":
		spec.Listen = normalizeListen(ec.Listen)
	case strings.TrimSpace(ec.SocksListen) == "" && !ec.ServeExit && ec.Originate.Kind == 0:
		spec.Listen = DefaultEgressListen
	}
	if strings.TrimSpace(ec.SocksListen) != "" {
		spec.SocksListen = normalizeListen(ec.SocksListen)
	}
	used := map[string]string{}
	for _, c := range connects {
		if c.Kind == store.KindTCP {
			used[c.Listen] = "connect " + strconv.Quote(c.Name)
		}
	}
	for _, l := range []struct{ key, raw, addr string }{{"listen", ec.Listen, spec.Listen}, {"socks_listen", ec.SocksListen, spec.SocksListen}} {
		if l.addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(l.addr); err != nil {
			p.addf(n.Line, "egress: %s %q must be a port or host:port", l.key, l.raw)
			continue
		}
		if owner, taken := used[l.addr]; taken {
			p.addf(n.Line, "egress: %s %s is already used by %s", l.key, l.addr, owner)
		}
		used[l.addr] = "egress " + l.key
	}
	spec.Originate = p.parseOriginate(&ec.Originate, spec.RequireAuth, used)
	for _, c := range ec.ExitAllow {
		cidr := strings.TrimSpace(c)
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			p.addf(n.Line, "egress: exit_allow %q is not a CIDR", c)
			continue
		}
		spec.ExitAllow = append(spec.ExitAllow, cidr)
	}
	switch {
	case len(ec.ExitAllow) > 0 && !spec.ServeExit:
		p.addf(n.Line, "egress: exit_allow needs serve_exit")
	case spec.RequireAuth && spec.SocksListen != "":
		p.addf(n.Line, "egress: require_auth cannot cover socks_listen: SOCKS5 passwords are capped at 255 bytes, shorter than mishmesh tokens")
	case spec.RequireAuth && spec.Listen == "":
		p.addf(n.Line, "egress: require_auth needs listen")
	}
	return spec
}

func (p *configParser) parseOriginate(n *yaml.Node, requireAuth bool, used map[string]string) []OriginateSpec {
	if n.Kind == 0 || n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		p.addf(n.Line, "egress: originate must be a mapping of name to originate definition")
		return nil
	}
	var out []OriginateSpec
	for i := 0; i < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		name := key.Value
		if !tunnelName.MatchString(name) {
			p.addf(key.Line, "egress: originate name %q is invalid (use letters, digits, '-' and '_')", name)
			continue
		}
		if val.Kind != yaml.MappingNode {
			p.addf(val.Line, "egress: originate %q must be a mapping", name)
			continue
		}
		p.checkKeys(val, originateKeys, "egress originate "+strconv.Quote(name))
		var oc originateConfig
		if err := val.Decode(&oc); err != nil {
			p.addf(val.Line, "egress: originate %q: %v", name, err)
			continue
		}
		spec := OriginateSpec{Name: name, Listen: normalizeListen(oc.Listen), Upstream: strings.TrimSpace(oc.Upstream), Token: strings.TrimSpace(oc.Token)}
		if spec.Listen == "" {
			p.addf(val.Line, "egress: originate %q needs listen", name)
			continue
		}
		if _, _, err := net.SplitHostPort(spec.Listen); err != nil {
			p.addf(val.Line, "egress: originate %q listen %q must be a port or host:port", name, oc.Listen)
			continue
		}
		if owner, taken := used[spec.Listen]; taken {
			p.addf(val.Line, "egress: originate %q listen %s is already used by %s", name, spec.Listen, owner)
			continue
		}
		used[spec.Listen] = "egress originate " + strconv.Quote(name)
		if _, err := parseOriginTarget(spec.Upstream); err != nil {
			p.addf(val.Line, "egress: originate %q upstream %q %v", name, oc.Upstream, err)
			continue
		}
		if requireAuth && spec.Token == "" {
			p.addf(val.Line, "egress: originate %q needs token because require_auth is set", name)
			continue
		}
		out = append(out, spec)
	}
	return out
}

type connectConfig struct {
	Endpoint    string `yaml:"endpoint"`
	Proto       string `yaml:"proto"`
	Listen      string `yaml:"listen"`
	IdleTimeout string `yaml:"idle_timeout"`
}

func (p *configParser) parseConnects(n *yaml.Node) []ConnectSpec {
	seen := make(map[string]bool)
	listens := make(map[string]string)
	var out []ConnectSpec
	for i := 0; i < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		name := key.Value
		fail := func(format string, args ...any) {
			p.addf(key.Line, "connect %q: %s", name, fmt.Sprintf(format, args...))
		}
		if !tunnelName.MatchString(name) {
			p.addf(key.Line, "connect name %q is invalid (use letters, digits, '-' and '_')", name)
			continue
		}
		if seen[name] {
			p.addf(key.Line, "connect %q is defined twice", name)
			continue
		}
		seen[name] = true
		if val.Kind != yaml.MappingNode {
			p.addf(val.Line, "connect %q must be a mapping", name)
			continue
		}
		p.checkKeys(val, connectKeys, fmt.Sprintf("connect %q", name))
		var cc connectConfig
		if err := val.Decode(&cc); err != nil {
			fail("%s", strings.ReplaceAll(strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n  "), "\n  ", "; "))
			continue
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
	}
	return out
}

func normalizeListen(listen string) string {
	listen = strings.TrimSpace(listen)
	if isDigits(listen) {
		return "127.0.0.1:" + strings.TrimPrefix(listen, ":")
	}
	return listen
}

func (p *configParser) parseTunnels(n *yaml.Node) []NamedTunnel {
	seen := make(map[string]bool)
	var out []NamedTunnel
	for i := 0; i < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		name := key.Value
		if !tunnelName.MatchString(name) {
			p.addf(key.Line, "tunnel name %q is invalid (use letters, digits, '-' and '_')", name)
			continue
		}
		if seen[name] {
			p.addf(key.Line, "tunnel %q is defined twice", name)
			continue
		}
		seen[name] = true
		if val.Kind != yaml.MappingNode {
			p.addf(val.Line, "tunnel %q must be a mapping", name)
			continue
		}
		p.checkKeys(val, tunnelKeys, fmt.Sprintf("tunnel %q", name))
		var tc TunnelConfig
		if err := val.Decode(&tc); err != nil {
			p.addf(val.Line, "tunnel %q: %s", name, strings.ReplaceAll(strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n  "), "\n  ", "; "))
			continue
		}
		nt := NamedTunnel{Name: name, TunnelConfig: tc, line: key.Line}
		p.validateTunnel(nt)
		out = append(out, nt)
	}
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
		if _, err := encodePolicy(t.Policy); err != nil {
			fail("policy: %v", err)
		}
	}
}

func isDigits(s string) bool {
	s = strings.TrimPrefix(s, ":")
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hashBasicAuthPassword(m map[string]any) (map[string]any, error) {
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
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash basic_auth_password: %w", err)
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if k != "basic_auth_password" {
			out[k] = val
		}
	}
	out["basic_auth_hash"] = string(hash)
	return out, nil
}

func encodePolicy(m map[string]any) (json.RawMessage, error) {
	m, err := hashBasicAuthPassword(m)
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
		raw, err := encodePolicy(t.Policy)
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
