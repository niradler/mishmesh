package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mishmesh/mishmesh/internal/store"
)

type FileConfig struct {
	Gateway  string
	Token    string
	Allow    []string
	LogLevel string
	Tunnels  []NamedTunnel
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
	envRef        = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)
	tunnelName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	rootKeys      = []string{"gateway", "token", "allow", "log_level", "tunnels"}
	tunnelKeys    = []string{"proto", "addr", "subdomain", "domain", "port", "reserved", "target_https", "insecure", "policy"}
	validProtos   = []string{store.KindHTTP, store.KindTCP, store.KindTLS}
	validProtoMsg = strings.Join(validProtos, ", ")
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
		Gateway  string    `yaml:"gateway"`
		Token    string    `yaml:"token"`
		Allow    []string  `yaml:"allow"`
		LogLevel string    `yaml:"log_level"`
		Tunnels  yaml.Node `yaml:"tunnels"`
	}
	if err := root.Decode(&raw); err != nil {
		p.addf(root.Line, "%s", strings.ReplaceAll(strings.TrimPrefix(err.Error(), "yaml: "), "\n", "; "))
		return nil
	}
	cfg := &FileConfig{Gateway: raw.Gateway, Token: raw.Token, Allow: raw.Allow, LogLevel: raw.LogLevel}
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
	case raw.Tunnels.Kind == 0:
		p.addf(root.Line, "tunnels: at least one tunnel is required")
	case raw.Tunnels.Kind != yaml.MappingNode:
		p.addf(raw.Tunnels.Line, "tunnels must be a mapping of name to tunnel definition")
	case len(raw.Tunnels.Content) == 0:
		p.addf(raw.Tunnels.Line, "tunnels: at least one tunnel is required")
	default:
		cfg.Tunnels = p.parseTunnels(&raw.Tunnels)
	}
	return cfg
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
	case store.KindTCP:
		if t.Subdomain != "" || t.Domain != "" {
			fail("subdomain and domain do not apply to tcp tunnels (use port)")
		}
		if t.TargetHTTPS {
			fail("target_https does not apply to tcp tunnels")
		}
		if t.Addr != "" && !strings.Contains(strings.TrimPrefix(t.Addr, "tcp://"), ":") && !isDigits(t.Addr) {
			fail("addr %q must be a port or host:port", t.Addr)
		}
	case store.KindHTTP, store.KindTLS:
		if t.Port != 0 {
			fail("port only applies to tcp tunnels")
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

func encodePolicy(m map[string]any) (json.RawMessage, error) {
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
