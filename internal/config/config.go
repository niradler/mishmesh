package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Server struct {
	IngressAddr           string
	APIAddr               string
	BaseDomain            string
	PublicScheme          string
	DataDSN               string
	AuthEnabled           bool
	AuthPasswordEnabled   bool
	WebUIEnabled          bool
	IngressEnabled        bool
	TLSEnabled            bool
	HTTPSAddr             string
	TLSCertFile           string
	TLSKeyFile            string
	ACMEEnabled           bool
	ACMEEmail             string
	ACMECacheDir          string
	TCPEnabled            bool
	TCPBindHost           string
	TCPPortMin            int
	TCPPortMax            int
	TLSPassthroughAddr    string
	TLSPassthroughEnabled bool
	SSHEnabled            bool
	SSHAddr               string
	SSHHostKeyFile        string
	ProxyAllowLoopback    bool
	SelfSignedTLS         bool
	BootstrapToken        string
	APIAuthToken          string
	APIAuthDisabled       bool
	LogLevel              string

	DataBackend string
	ConnBackend string
	RedisURL    string

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

	QuotaMaxAgents         int
	QuotaMaxEndpoints      int
	QuotaMaxBandwidthBytes int64
}

type Agent struct {
	GatewayURL string
	Token      string
	LogLevel   string
	Allow      string
}

const envPrefix = "MISHMESH_"

func LoadServer() Server {
	return Server{
		IngressAddr:           env("INGRESS_ADDR", "127.0.0.1:8080"),
		APIAddr:               env("API_ADDR", "127.0.0.1:8081"),
		BaseDomain:            env("BASE_DOMAIN", "localhost:8080"),
		PublicScheme:          env("PUBLIC_SCHEME", "http"),
		DataDSN:               env("DATA_DSN", "mishmesh.db"),
		AuthEnabled:           envBool("AUTH_ENABLED", false),
		AuthPasswordEnabled:   envBool("AUTH_PASSWORD_ENABLED", true),
		WebUIEnabled:          envBool("WEBUI_ENABLED", false),
		IngressEnabled:        envBool("INGRESS_ENABLED", true),
		TLSEnabled:            envBool("TLS_ENABLED", false),
		HTTPSAddr:             env("HTTPS_ADDR", "127.0.0.1:8443"),
		TLSCertFile:           env("TLS_CERT_FILE", ""),
		TLSKeyFile:            env("TLS_KEY_FILE", ""),
		ACMEEnabled:           envBool("ACME_ENABLED", false),
		ACMEEmail:             env("ACME_EMAIL", ""),
		ACMECacheDir:          env("ACME_CACHE_DIR", "./certs"),
		TCPEnabled:            envBool("TCP_ENABLED", true),
		TCPBindHost:           env("TCP_BIND_HOST", "127.0.0.1"),
		TCPPortMin:            envInt("TCP_PORT_MIN", 10000),
		TCPPortMax:            envInt("TCP_PORT_MAX", 10100),
		TLSPassthroughAddr:    env("TLS_PASSTHROUGH_ADDR", "127.0.0.1:8444"),
		TLSPassthroughEnabled: envBool("TLS_PASSTHROUGH_ENABLED", false),
		SSHEnabled:            envBool("SSH_ENABLED", false),
		SSHAddr:               env("SSH_ADDR", "127.0.0.1:2222"),
		SSHHostKeyFile:        env("SSH_HOST_KEY_FILE", ""),
		ProxyAllowLoopback:    envBool("PROXY_ALLOW_LOOPBACK", false),
		SelfSignedTLS:         envBool("SELF_SIGNED_TLS", false),
		BootstrapToken:        env("BOOTSTRAP_TOKEN", ""),
		APIAuthToken:          env("API_AUTH_TOKEN", ""),
		APIAuthDisabled:       envBool("API_AUTH_DISABLED", false),
		LogLevel:              env("LOG_LEVEL", "info"),

		DataBackend: env("DATA_BACKEND", ""),
		ConnBackend: env("CONN_BACKEND", "memory"),
		RedisURL:    env("REDIS_URL", ""),

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

		QuotaMaxAgents:         envInt("QUOTA_MAX_AGENTS", 0),
		QuotaMaxEndpoints:      envInt("QUOTA_MAX_ENDPOINTS", 0),
		QuotaMaxBandwidthBytes: int64(envInt("QUOTA_MAX_BANDWIDTH_BYTES", 0)),
	}
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
	if s.APIAuthToken == "" && !s.APIAuthDisabled {
		return fmt.Errorf("config: API_AUTH_TOKEN must be set to protect the control API (or set API_AUTH_DISABLED=true to explicitly run it without auth)")
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
