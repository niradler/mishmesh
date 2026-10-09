package config

import (
	"fmt"
	"os"
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
}

type Agent struct {
	GatewayURL string
	Token      string
	LogLevel   string
	Allow      string
}

const envPrefix = "MISHMESH_"

func LoadServer() Server {
	signupMode := env("SIGNUP_MODE", "org")
	return Server{
		IngressAddr:              env("INGRESS_ADDR", "127.0.0.1:8080"),
		APIAddr:                  env("API_ADDR", "127.0.0.1:8081"),
		BaseDomain:               env("BASE_DOMAIN", "localhost:8080"),
		PublicScheme:             env("PUBLIC_SCHEME", "http"),
		DataDSN:                  env("DATA_DSN", "mishmesh.db"),
		AuthEnabled:              envBool("AUTH_ENABLED", false),
		AuthPasswordEnabled:      envBool("AUTH_PASSWORD_ENABLED", true),
		SignupMode:               signupMode,
		DomainVerification:       envBool("DOMAIN_VERIFICATION", signupMode == "org"),
		PathRouting:              envBool("PATH_ROUTING", signupMode != "org"),
		MetricsToken:             env("METRICS_TOKEN", ""),
		WebUIEnabled:             envBool("WEBUI_ENABLED", false),
		IngressEnabled:           envBool("INGRESS_ENABLED", true),
		TLSEnabled:               envBool("TLS_ENABLED", false),
		HTTPSAddr:                env("HTTPS_ADDR", "127.0.0.1:8443"),
		TLSCertFile:              env("TLS_CERT_FILE", ""),
		TLSKeyFile:               env("TLS_KEY_FILE", ""),
		ACMEEnabled:              envBool("ACME_ENABLED", false),
		ACMEEmail:                env("ACME_EMAIL", ""),
		ACMECacheDir:             env("ACME_CACHE_DIR", "./certs"),
		TCPEnabled:               envBool("TCP_ENABLED", true),
		TCPBindHost:              env("TCP_BIND_HOST", "127.0.0.1"),
		TCPPortMin:               envInt("TCP_PORT_MIN", 10000),
		TCPPortMax:               envInt("TCP_PORT_MAX", 10100),
		TLSPassthroughAddr:       env("TLS_PASSTHROUGH_ADDR", "127.0.0.1:8444"),
		TLSPassthroughEnabled:    envBool("TLS_PASSTHROUGH_ENABLED", false),
		TLSPassthroughPublicPort: envInt("TLS_PASSTHROUGH_PUBLIC_PORT", 8444),
		SSHEnabled:               envBool("SSH_ENABLED", false),
		SSHAddr:                  env("SSH_ADDR", "127.0.0.1:2222"),
		SSHHostKeyFile:           env("SSH_HOST_KEY_FILE", ""),
		ProxyAllowLoopback:       envBool("PROXY_ALLOW_LOOPBACK", false),
		ProxyAllowPrivate:        envBool("PROXY_ALLOW_PRIVATE", false),
		ProxyAllowedCIDRs:        envList("PROXY_ALLOWED_CIDRS"),
		SelfSignedTLS:            envBool("SELF_SIGNED_TLS", false),
		BootstrapToken:           env("BOOTSTRAP_TOKEN", ""),
		APIAuthToken:             env("API_AUTH_TOKEN", ""),
		APIAuthDisabled:          envBool("API_AUTH_DISABLED", false),
		LogLevel:                 env("LOG_LEVEL", "info"),

		DataBackend:             env("DATA_BACKEND", ""),
		DataMaxConns:            envInt("DATA_MAX_CONNS", 25),
		DataMaxIdleConns:        envInt("DATA_MAX_IDLE_CONNS", 0),
		DataConnMaxLifetime:     envDuration("DATA_CONN_MAX_LIFETIME", 30*time.Minute),
		DataConnMaxIdleTime:     envDuration("DATA_CONN_MAX_IDLE_TIME", 5*time.Minute),
		IngressCacheTTL:         envDuration("INGRESS_CACHE_TTL", 2*time.Second),
		UpstreamResponseTimeout: upstreamResponseTimeout(),
		PprofAddr:               env("PPROF_ADDR", ""),
		ConnBackend:             env("CONN_BACKEND", "memory"),
		RedisURL:                env("REDIS_URL", ""),
		RedisPoolSize:           envInt("REDIS_POOL_SIZE", 0),

		MetricsEnabled: envBool("METRICS_ENABLED", true),
		ReachInEnabled: envBool("REACHIN_ENABLED", false),

		WebUIDir:        env("WEBUI_DIR", ""),
		SessionTTLHours: envInt("SESSION_TTL_HOURS", 168),

		OIDCIssuer:         env("OIDC_ISSUER", "https://accounts.google.com"),
		GoogleClientID:     env("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: env("GOOGLE_CLIENT_SECRET", ""),
		OIDCRedirectURL:    env("OIDC_REDIRECT_URL", ""),
		EndpointOIDCKey:    env("ENDPOINT_OIDC_KEY", ""),
		OIDCAllowPrivate:   envBool("OIDC_ALLOW_PRIVATE_ISSUERS", false),

		MaxOrgsPerUser:         envInt("MAX_ORGS_PER_USER", 3),
		QuotaMaxAgents:         envInt("QUOTA_MAX_AGENTS", 0),
		QuotaMaxEndpoints:      envInt("QUOTA_MAX_ENDPOINTS", 0),
		QuotaMaxBandwidthBytes: int64(envInt("QUOTA_MAX_BANDWIDTH_BYTES", 0)),

		ClusterEnabled: envBool("CLUSTER_ENABLED", false),
		NodeID:         env("NODE_ID", hostnameOrEmpty()),
		RelayAddr:      env("RELAY_ADDR", "127.0.0.1:7443"),
		RelayAdvertise: env("RELAY_ADVERTISE", ""),
		ClusterSecret:  env("CLUSTER_SECRET", ""),

		TrustedProxies: env("TRUSTED_PROXIES", ""),

		AllowedOrigins: envList("ALLOWED_ORIGINS"),
	}
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

func (s Server) Validate() error {
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

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(envPrefix + key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(envPrefix + key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(envPrefix + key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d < 0 {
		return def
	}
	return d
}

func upstreamResponseTimeout() time.Duration {
	value := env("UPSTREAM_RESPONSE_TIMEOUT", "5m")
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return -1
	}
	return duration
}
