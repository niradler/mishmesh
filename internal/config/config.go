package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Server struct {
	IngressAddr         string
	APIAddr             string
	BaseDomain          string
	PublicScheme        string
	DataDSN             string
	AuthEnabled         bool
	AuthPasswordEnabled bool
	WebUIEnabled        bool
	IngressEnabled      bool
	TLSEnabled          bool
	HTTPSAddr           string
	TLSCertFile         string
	TLSKeyFile          string
	ACMEEnabled         bool
	ACMEEmail           string
	ACMECacheDir        string
	LogLevel            string
}

type Agent struct {
	GatewayURL string
	Token      string
	LogLevel   string
}

const envPrefix = "MISHMESH_"

func LoadServer() Server {
	return Server{
		IngressAddr:         env("INGRESS_ADDR", "127.0.0.1:8080"),
		APIAddr:             env("API_ADDR", "127.0.0.1:8081"),
		BaseDomain:          env("BASE_DOMAIN", "localhost:8080"),
		PublicScheme:        env("PUBLIC_SCHEME", "http"),
		DataDSN:             env("DATA_DSN", "mishmesh.db"),
		AuthEnabled:         envBool("AUTH_ENABLED", false),
		AuthPasswordEnabled: envBool("AUTH_PASSWORD_ENABLED", true),
		WebUIEnabled:        envBool("WEBUI_ENABLED", false),
		IngressEnabled:      envBool("INGRESS_ENABLED", true),
		TLSEnabled:          envBool("TLS_ENABLED", false),
		HTTPSAddr:           env("HTTPS_ADDR", "127.0.0.1:8443"),
		TLSCertFile:         env("TLS_CERT_FILE", ""),
		TLSKeyFile:          env("TLS_KEY_FILE", ""),
		ACMEEnabled:         envBool("ACME_ENABLED", false),
		ACMEEmail:           env("ACME_EMAIL", ""),
		ACMECacheDir:        env("ACME_CACHE_DIR", "./certs"),
		LogLevel:            env("LOG_LEVEL", "info"),
	}
}

func LoadAgent() Agent {
	return Agent{
		GatewayURL: env("GATEWAY_URL", "ws://localhost:8081"),
		Token:      env("TOKEN", ""),
		LogLevel:   env("LOG_LEVEL", "info"),
	}
}

func (s Server) Validate() error {
	if s.BaseDomain == "" {
		return fmt.Errorf("config: BASE_DOMAIN is required")
	}
	if s.PublicScheme != "http" && s.PublicScheme != "https" {
		return fmt.Errorf("config: PUBLIC_SCHEME must be http or https, got %q", s.PublicScheme)
	}
	return nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(envPrefix + key); ok {
		return v
	}
	return def
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
