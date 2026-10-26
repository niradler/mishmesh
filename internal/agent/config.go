package agent

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
		paths = append(paths, filepath.Join(home, ".mishmesh", "config.yaml"))
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
