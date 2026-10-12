package cloudflare

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL      = "https://api.cloudflare.com/client/v4"
	DefaultSyncInterval = 5 * time.Minute
	DefaultOrg          = "org_default"
	DefaultMetricsAddr  = "127.0.0.1:20241"
	DefaultCloudflared  = "cloudflared"
)

type Config struct {
	APIToken     string
	AccountID    string
	TunnelID     string
	Origin       string
	AccessAppID  string
	Prune        bool
	SyncInterval time.Duration
	Orgs         []string
	BaseURL      string
	Cloudflared  *SupervisorConfig
}

type SupervisorConfig struct {
	Bin         string
	TokenFile   string
	MetricsAddr string
}

func ConfigFromEnv(getenv func(string) string, ingressAddr string) (*Config, error) {
	token := strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_API_TOKEN"))
	if token == "" {
		return nil, nil
	}
	cfg := &Config{
		APIToken:     token,
		AccountID:    strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_ACCOUNT_ID")),
		TunnelID:     strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_TUNNEL_ID")),
		Origin:       strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_ORIGIN")),
		AccessAppID:  strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_ACCESS_APP_ID")),
		BaseURL:      strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_BASE_URL")),
		SyncInterval: DefaultSyncInterval,
	}
	if cfg.AccountID == "" {
		return nil, errors.New("MISHMESH_CLOUDFLARE_ACCOUNT_ID is required when MISHMESH_CLOUDFLARE_API_TOKEN is set")
	}
	if cfg.TunnelID == "" {
		return nil, errors.New("MISHMESH_CLOUDFLARE_TUNNEL_ID is required when MISHMESH_CLOUDFLARE_API_TOKEN is set")
	}
	if cfg.Origin == "" {
		addr := strings.TrimSpace(ingressAddr)
		if addr == "" {
			return nil, errors.New("MISHMESH_CLOUDFLARE_ORIGIN is required when the ingress address is empty")
		}
		cfg.Origin = "http://" + addr
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if v := strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_PRUNE")); v != "" {
		prune, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("MISHMESH_CLOUDFLARE_PRUNE: %w", err)
		}
		cfg.Prune = prune
	}
	if v := strings.TrimSpace(getenv("MISHMESH_CLOUDFLARE_SYNC_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("MISHMESH_CLOUDFLARE_SYNC_INTERVAL: %w", err)
		}
		if d <= 0 {
			return nil, errors.New("MISHMESH_CLOUDFLARE_SYNC_INTERVAL must be positive")
		}
		cfg.SyncInterval = d
	}
	cfg.Orgs = splitList(getenv("MISHMESH_CLOUDFLARE_ORGS"))
	if len(cfg.Orgs) == 0 {
		cfg.Orgs = []string{DefaultOrg}
	}
	if v := strings.TrimSpace(getenv("MISHMESH_CLOUDFLARED_RUN")); v != "" {
		run, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("MISHMESH_CLOUDFLARED_RUN: %w", err)
		}
		if run {
			sup := &SupervisorConfig{
				Bin:         strings.TrimSpace(getenv("MISHMESH_CLOUDFLARED_BIN")),
				TokenFile:   strings.TrimSpace(getenv("MISHMESH_CLOUDFLARED_TOKEN_FILE")),
				MetricsAddr: strings.TrimSpace(getenv("MISHMESH_CLOUDFLARED_METRICS_ADDR")),
			}
			if sup.Bin == "" {
				sup.Bin = DefaultCloudflared
			}
			if sup.MetricsAddr == "" {
				sup.MetricsAddr = DefaultMetricsAddr
			}
			if sup.TokenFile == "" {
				return nil, errors.New("MISHMESH_CLOUDFLARED_TOKEN_FILE is required when MISHMESH_CLOUDFLARED_RUN is true")
			}
			cfg.Cloudflared = sup
		}
	}
	return cfg, nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
