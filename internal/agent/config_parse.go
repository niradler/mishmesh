package agent

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

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

func yamlProblem(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n")
	lines := strings.Split(msg, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "; ")
}

func (p *configParser) optionalMapping(n *yaml.Node, notMappingMsg string) bool {
	if n.Kind == 0 || n.Tag == "!!null" {
		return false
	}
	if n.Kind != yaml.MappingNode {
		p.addf(n.Line, "%s", notMappingMsg)
		return false
	}
	return true
}

func (p *configParser) eachNamed(n *yaml.Node, scope, kind string, allowed []string, visit func(name string, key, val *yaml.Node)) {
	prefix, label := "", kind
	if scope != "" {
		prefix, label = scope+": ", scope+" "+kind
	}
	seen := make(map[string]bool)
	for i := 0; i < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		name := key.Value
		if !tunnelName.MatchString(name) {
			p.addf(key.Line, "%s%s name %q is invalid (use letters, digits, '-' and '_')", prefix, kind, name)
			continue
		}
		if seen[name] {
			p.addf(key.Line, "%s%s %q is defined twice", prefix, kind, name)
			continue
		}
		seen[name] = true
		if val.Kind != yaml.MappingNode {
			p.addf(val.Line, "%s%s %q must be a mapping", prefix, kind, name)
			continue
		}
		p.checkKeys(val, allowed, fmt.Sprintf("%s %q", label, name))
		visit(name, key, val)
	}
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
		p.addf(root.Line, "%s", yamlProblem(err))
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
	if p.optionalMapping(&raw.Tunnels, "tunnels must be a mapping of name to tunnel definition") {
		cfg.Tunnels = p.parseTunnels(&raw.Tunnels)
	}
	if p.optionalMapping(&raw.Connects, "connects must be a mapping of name to connect definition") {
		cfg.Connects = p.parseConnects(&raw.Connects)
	}
	if p.optionalMapping(&raw.Egress, "egress must be a mapping") {
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

func normalizeListen(listen string) string {
	listen = strings.TrimSpace(listen)
	if isDigits(listen) {
		return "127.0.0.1:" + strings.TrimPrefix(listen, ":")
	}
	return listen
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
