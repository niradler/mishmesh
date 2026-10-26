package config

import (
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	IngressAddr              string
	APIAddr                  string
	BaseDomain               string
	PublicScheme             string
	DataDSN                  string
	AuthEnabled              bool
	AuthPasswordEnabled      bool
	SignupMode               string
	DomainVerification       bool
	PathRouting              bool
	MetricsToken             string
	WebUIEnabled             bool
	IngressEnabled           bool
	TLSEnabled               bool
	HTTPSAddr                string
	TLSCertFile              string
	TLSKeyFile               string
	ACMEEnabled              bool
	ACMEEmail                string
	ACMECacheDir             string
	TCPEnabled               bool
	TCPBindHost              string
	TCPPortMin               int
	TCPPortMax               int
	UDPBindHost              string
	UDPPortMin               int
	UDPPortMax               int
	UDPIdleTimeout           time.Duration
	UDPMaxFlows              int
	AccessLogRetention       time.Duration
	TLSPassthroughAddr       string
	TLSPassthroughEnabled    bool
	TLSPassthroughPublicPort int
	SSHEnabled               bool
	SSHAddr                  string
	SSHHostKeyFile           string
	ProxyAllowLoopback       bool
	ProxyAllowPrivate        bool
	ProxyAllowedCIDRs        []string
	SelfSignedTLS            bool
	BootstrapToken           string
	APIAuthToken             string
	APIAuthDisabled          bool
	LogLevel                 string

	DataBackend             string
	DataMaxConns            int
	DataMaxIdleConns        int
	DataConnMaxLifetime     time.Duration
	DataConnMaxIdleTime     time.Duration
	DataQueryTimeout        time.Duration
	IngressCacheTTL         time.Duration
	UpstreamResponseTimeout time.Duration
	PprofAddr               string
	ConnBackend             string
	RedisURL                string
	RedisPoolSize           int

	MetricsEnabled bool
	ReachInEnabled bool

	WebUIDir        string
	SessionTTLHours int

	OIDCIssuer         string
	GoogleClientID     string
	GoogleClientSecret string
	OIDCRedirectURL    string
	EndpointOIDCKey    string
	OIDCAllowPrivate   bool

	MaxOrgsPerUser         int
	QuotaMaxAgents         int
	QuotaMaxEndpoints      int
	QuotaMaxBandwidthBytes int64

	ClusterEnabled bool
	NodeID         string
	RelayAddr      string
	RelayAdvertise string
	ClusterSecret  string

	TrustedProxies string

	AllowedOrigins []string

	HealthInterval time.Duration
	HealthTimeout  time.Duration

	SecretKeyFile  string
	TokenKeyFile   string
	APIPublicURL   string
	DockerHost     string
	HostCommands   bool
	CloudflaredBin string

	envErrors []string
}

type Agent struct {
	GatewayURL string
	Token      string
	LogLevel   string
	Allow      string
}

const envPrefix = "MISHMESH_"

func LoadServer() Server {
	l := &loader{}
	signupMode := env("SIGNUP_MODE", "org")
	s := Server{
		IngressAddr:              env("INGRESS_ADDR", "127.0.0.1:8080"),
		APIAddr:                  env("API_ADDR", "127.0.0.1:8081"),
		BaseDomain:               env("BASE_DOMAIN", "localhost:8080"),
		PublicScheme:             env("PUBLIC_SCHEME", "http"),
		DataDSN:                  env("DATA_DSN", "mishmesh.db"),
		AuthEnabled:              l.envBool("AUTH_ENABLED", false),
		AuthPasswordEnabled:      l.envBool("AUTH_PASSWORD_ENABLED", true),
		SignupMode:               signupMode,
		DomainVerification:       l.envBool("DOMAIN_VERIFICATION", signupMode == "org"),
		PathRouting:              l.envBool("PATH_ROUTING", signupMode != "org"),
		MetricsToken:             env("METRICS_TOKEN", ""),
		WebUIEnabled:             l.envBool("WEBUI_ENABLED", false),
		IngressEnabled:           l.envBool("INGRESS_ENABLED", true),
		TLSEnabled:               l.envBool("TLS_ENABLED", false),
		HTTPSAddr:                env("HTTPS_ADDR", "127.0.0.1:8443"),
		TLSCertFile:              env("TLS_CERT_FILE", ""),
		TLSKeyFile:               env("TLS_KEY_FILE", ""),
		ACMEEnabled:              l.envBool("ACME_ENABLED", false),
		ACMEEmail:                env("ACME_EMAIL", ""),
		ACMECacheDir:             env("ACME_CACHE_DIR", "./certs"),
		TCPEnabled:               l.envBool("TCP_ENABLED", true),
		TCPBindHost:              env("TCP_BIND_HOST", "127.0.0.1"),
		TCPPortMin:               l.envInt("TCP_PORT_MIN", 10000),
		TCPPortMax:               l.envInt("TCP_PORT_MAX", 10100),
		UDPBindHost:              env("UDP_BIND_HOST", "127.0.0.1"),
		UDPPortMin:               l.envInt("UDP_PORT_MIN", 10200),
		UDPPortMax:               l.envInt("UDP_PORT_MAX", 10300),
		UDPIdleTimeout:           l.envDuration("UDP_IDLE_TIMEOUT", 60*time.Second),
		UDPMaxFlows:              l.envInt("UDP_MAX_FLOWS", 1024),
		AccessLogRetention:       l.envDuration("ACCESS_LOG_RETENTION", 168*time.Hour),
		TLSPassthroughAddr:       env("TLS_PASSTHROUGH_ADDR", "127.0.0.1:8444"),
		TLSPassthroughEnabled:    l.envBool("TLS_PASSTHROUGH_ENABLED", false),
		TLSPassthroughPublicPort: l.envInt("TLS_PASSTHROUGH_PUBLIC_PORT", 8444),
		SSHEnabled:               l.envBool("SSH_ENABLED", false),
		SSHAddr:                  env("SSH_ADDR", "127.0.0.1:2222"),
		SSHHostKeyFile:           env("SSH_HOST_KEY_FILE", ""),
		ProxyAllowLoopback:       l.envBool("PROXY_ALLOW_LOOPBACK", false),
		ProxyAllowPrivate:        l.envBool("PROXY_ALLOW_PRIVATE", false),
		ProxyAllowedCIDRs:        envList("PROXY_ALLOWED_CIDRS"),
		SelfSignedTLS:            l.envBool("SELF_SIGNED_TLS", false),
		BootstrapToken:           env("BOOTSTRAP_TOKEN", ""),
		APIAuthToken:             env("API_AUTH_TOKEN", ""),
		APIAuthDisabled:          l.envBool("API_AUTH_DISABLED", false),
		LogLevel:                 env("LOG_LEVEL", "info"),

		DataBackend:             env("DATA_BACKEND", ""),
		DataMaxConns:            l.envInt("DATA_MAX_CONNS", 25),
		DataMaxIdleConns:        l.envInt("DATA_MAX_IDLE_CONNS", 0),
		DataConnMaxLifetime:     l.envDuration("DATA_CONN_MAX_LIFETIME", 30*time.Minute),
		DataConnMaxIdleTime:     l.envDuration("DATA_CONN_MAX_IDLE_TIME", 5*time.Minute),
		DataQueryTimeout:        strictDuration("DATA_QUERY_TIMEOUT", "5s"),
		IngressCacheTTL:         l.envDuration("INGRESS_CACHE_TTL", 2*time.Second),
		UpstreamResponseTimeout: strictDuration("UPSTREAM_RESPONSE_TIMEOUT", "5m"),
		PprofAddr:               env("PPROF_ADDR", ""),
		ConnBackend:             env("CONN_BACKEND", "memory"),
		RedisURL:                env("REDIS_URL", ""),
		RedisPoolSize:           l.envInt("REDIS_POOL_SIZE", 0),

		MetricsEnabled: l.envBool("METRICS_ENABLED", true),
		ReachInEnabled: l.envBool("REACHIN_ENABLED", false),

		WebUIDir:        env("WEBUI_DIR", ""),
		SessionTTLHours: l.envInt("SESSION_TTL_HOURS", 168),

		OIDCIssuer:         env("OIDC_ISSUER", "https://accounts.google.com"),
		GoogleClientID:     env("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: env("GOOGLE_CLIENT_SECRET", ""),
		OIDCRedirectURL:    env("OIDC_REDIRECT_URL", ""),
		EndpointOIDCKey:    env("ENDPOINT_OIDC_KEY", ""),
		OIDCAllowPrivate:   l.envBool("OIDC_ALLOW_PRIVATE_ISSUERS", false),

		MaxOrgsPerUser:         l.envInt("MAX_ORGS_PER_USER", 3),
		QuotaMaxAgents:         l.envInt("QUOTA_MAX_AGENTS", 0),
		QuotaMaxEndpoints:      l.envInt("QUOTA_MAX_ENDPOINTS", 0),
		QuotaMaxBandwidthBytes: int64(l.envInt("QUOTA_MAX_BANDWIDTH_BYTES", 0)),

		ClusterEnabled: l.envBool("CLUSTER_ENABLED", false),
		NodeID:         env("NODE_ID", hostnameOrEmpty()),
		RelayAddr:      env("RELAY_ADDR", "127.0.0.1:7443"),
		RelayAdvertise: env("RELAY_ADVERTISE", ""),
		ClusterSecret:  env("CLUSTER_SECRET", ""),

		TrustedProxies: env("TRUSTED_PROXIES", ""),

		AllowedOrigins: envList("ALLOWED_ORIGINS"),

		HealthInterval: strictDuration("HEALTH_INTERVAL", "15s"),
		HealthTimeout:  strictDuration("HEALTH_TIMEOUT", "3s"),

		SecretKeyFile:  env("SECRET_KEY_FILE", ""),
		TokenKeyFile:   env("TOKEN_KEY_FILE", ""),
		APIPublicURL:   env("API_PUBLIC_URL", ""),
		DockerHost:     env("DOCKER_HOST", ""),
		HostCommands:   l.envBool("HOST_COMMANDS", signupMode == "invite"),
		CloudflaredBin: env("CLOUDFLARED_BIN", ""),
	}
	s.envErrors = l.errs
	return s
}

func envList(key string) []string {
	var out []string
	for _, part := range strings.Split(env(key, ""), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func hostnameOrEmpty() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

const minClusterSecretLen = 32

func (s Server) EffectiveDataBackend() string {
	if s.DataBackend != "" {
		return s.DataBackend
	}
	if strings.HasPrefix(s.DataDSN, "postgres://") || strings.HasPrefix(s.DataDSN, "postgresql://") {
		return "postgres"
	}
	return "sqlite"
}

const secretKeyFileName = "mishmesh-secret.key"

func (s Server) EffectiveSecretKeyFile() string {
	if s.SecretKeyFile != "" {
		return s.SecretKeyFile
	}
	if s.EffectiveDataBackend() != "sqlite" {
		return secretKeyFileName
	}
	path := strings.TrimPrefix(s.DataDSN, "file:")
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" || path == ":memory:" {
		return secretKeyFileName
	}
	return filepath.Join(filepath.Dir(path), secretKeyFileName)
}

const tokenKeyFileName = "token-signing.key"

func (s Server) EffectiveTokenKeyFile() string {
	if s.TokenKeyFile != "" {
		return s.TokenKeyFile
	}
	return filepath.Join(filepath.Dir(s.EffectiveSecretKeyFile()), tokenKeyFileName)
}

func (s Server) validateCluster() error {
	if s.ConnBackend != "redis" {
		return fmt.Errorf("config: CLUSTER_ENABLED requires CONN_BACKEND=redis, got %q", s.ConnBackend)
	}
	if s.RedisURL == "" {
		return fmt.Errorf("config: CLUSTER_ENABLED requires REDIS_URL")
	}
	if s.EffectiveDataBackend() == "sqlite" {
		return fmt.Errorf("config: CLUSTER_ENABLED requires a shared data backend (postgres), not sqlite")
	}
	if s.NodeID == "" {
		return fmt.Errorf("config: CLUSTER_ENABLED requires NODE_ID (hostname lookup failed)")
	}
	if s.RelayAddr == "" {
		return fmt.Errorf("config: CLUSTER_ENABLED requires RELAY_ADDR")
	}
	if s.RelayAdvertise == "" {
		return fmt.Errorf("config: CLUSTER_ENABLED requires RELAY_ADVERTISE (host:port other nodes can reach, e.g. $(POD_IP):7443)")
	}
	if len(s.ClusterSecret) < minClusterSecretLen {
		return fmt.Errorf("config: CLUSTER_SECRET must be at least %d characters", minClusterSecretLen)
	}
	return nil
}

func LoadAgent() Agent {
	return Agent{
		GatewayURL: env("GATEWAY_URL", "ws://localhost:8081"),
		Token:      env("TOKEN", ""),
		LogLevel:   env("LOG_LEVEL", "info"),
		Allow:      env("ALLOW", ""),
	}
}

func validPortRange(min, max int) bool {
	return min >= 1 && max <= 65535 && min <= max
}

func (s Server) validateRanges() error {
	if s.TCPEnabled && !validPortRange(s.TCPPortMin, s.TCPPortMax) {
		return fmt.Errorf("config: TCP_PORT_MIN..TCP_PORT_MAX must be within 1-65535 with min <= max, got %d-%d", s.TCPPortMin, s.TCPPortMax)
	}
	if s.IngressEnabled && !validPortRange(s.UDPPortMin, s.UDPPortMax) {
		return fmt.Errorf("config: UDP_PORT_MIN..UDP_PORT_MAX must be within 1-65535 with min <= max, got %d-%d", s.UDPPortMin, s.UDPPortMax)
	}
	nonNegative := map[string]int64{
		"QUOTA_MAX_AGENTS":          int64(s.QuotaMaxAgents),
		"QUOTA_MAX_ENDPOINTS":       int64(s.QuotaMaxEndpoints),
		"QUOTA_MAX_BANDWIDTH_BYTES": s.QuotaMaxBandwidthBytes,
		"MAX_ORGS_PER_USER":         int64(s.MaxOrgsPerUser),
		"UDP_MAX_FLOWS":             int64(s.UDPMaxFlows),
		"SESSION_TTL_HOURS":         int64(s.SessionTTLHours),
		"REDIS_POOL_SIZE":           int64(s.RedisPoolSize),
	}
	for _, name := range slices.Sorted(maps.Keys(nonNegative)) {
		if nonNegative[name] < 0 {
			return fmt.Errorf("config: %s must not be negative", name)
		}
	}
	return nil
}

func (s Server) Validate() error {
	if len(s.envErrors) > 0 {
		return fmt.Errorf("config: %s", strings.Join(s.envErrors, "; "))
	}
	if err := s.validateRanges(); err != nil {
		return err
	}
	if s.BaseDomain == "" {
		return fmt.Errorf("config: BASE_DOMAIN is required")
	}
	if s.PublicScheme != "http" && s.PublicScheme != "https" {
		return fmt.Errorf("config: PUBLIC_SCHEME must be http or https, got %q", s.PublicScheme)
	}
	if s.SignupMode != "" && s.SignupMode != "org" && s.SignupMode != "invite" {
		return fmt.Errorf("config: SIGNUP_MODE must be org or invite, got %q", s.SignupMode)
	}
	if s.APIAuthToken == "" && !s.APIAuthDisabled {
		return fmt.Errorf("config: API_AUTH_TOKEN must be set to protect the control API (or set API_AUTH_DISABLED=true to explicitly run it without auth)")
	}
	if s.DataMaxConns < 0 || s.DataMaxIdleConns < 0 {
		return fmt.Errorf("config: DATA_MAX_CONNS and DATA_MAX_IDLE_CONNS must not be negative")
	}
	if s.UpstreamResponseTimeout <= 0 {
		return fmt.Errorf("config: UPSTREAM_RESPONSE_TIMEOUT must be a positive duration")
	}
	if s.HealthInterval <= 0 || s.HealthTimeout <= 0 {
		return fmt.Errorf("config: HEALTH_INTERVAL and HEALTH_TIMEOUT must be positive durations")
	}
	if s.DataQueryTimeout <= 0 {
		return fmt.Errorf("config: DATA_QUERY_TIMEOUT must be a positive duration")
	}
	if s.APIPublicURL != "" {
		u, err := url.Parse(s.APIPublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("config: API_PUBLIC_URL must be an absolute http(s) URL without query or fragment")
		}
	}
	if s.TLSPassthroughEnabled && (s.TLSPassthroughPublicPort < 1 || s.TLSPassthroughPublicPort > 65535) {
		return fmt.Errorf("config: TLS_PASSTHROUGH_PUBLIC_PORT must be between 1 and 65535")
	}
	if s.ClusterEnabled {
		return s.validateCluster()
	}
	return nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(envPrefix + key); ok {
		return v
	}
	return def
}

type loader struct {
	errs []string
}

func (l *loader) reject(key, value, want string) {
	l.errs = append(l.errs, fmt.Sprintf("%s%s=%q is not %s", envPrefix, key, value, want))
}

func (l *loader) envInt(key string, def int) int {
	v, ok := os.LookupEnv(envPrefix + key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		l.reject(key, v, "an integer")
		return def
	}
	return n
}

func (l *loader) envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(envPrefix + key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		l.reject(key, v, "a boolean")
		return def
	}
	return b
}

func (l *loader) envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(envPrefix + key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d < 0 {
		l.reject(key, v, "a non-negative duration")
		return def
	}
	return d
}

func strictDuration(name, defaultValue string) time.Duration {
	value := env(name, defaultValue)
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return -1
	}
	return duration
}
