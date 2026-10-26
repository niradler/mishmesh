package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL         = "https://api.cloudflare.com/client/v4"
	DefaultSyncInterval    = 5 * time.Minute
	DefaultImportedRefresh = time.Minute
	DefaultMetricsAddr     = "127.0.0.1:20241"
	DefaultCloudflared     = "cloudflared"

	LevelNone     = "none"
	LevelImported = "imported"
	LevelManaged  = "managed"

	ConnectorExternal = "external"
	ConnectorManaged  = "managed"

	TokenKindTunnel = "tunnel_token"
	TokenKindAPI    = "api_token"
)

type Config struct {
	ConnectionID   string
	OrgID          string
	Name           string
	AccountID      string
	TunnelID       string
	TunnelToken    string
	APIToken       string
	AccessAppID    string
	Origin         string
	Connector      string
	MetricsAddr    string
	Prune          bool
	SyncInterval   time.Duration
	BaseURL        string
	CloudflaredBin string
}

func (c *Config) Level() string {
	if c == nil {
		return LevelNone
	}
	if c.APIToken != "" {
		return LevelManaged
	}
	return LevelImported
}

func (c *Config) refreshInterval() time.Duration {
	if c.SyncInterval > 0 {
		return c.SyncInterval
	}
	if c.Level() == LevelManaged {
		return DefaultSyncInterval
	}
	return DefaultImportedRefresh
}

type SupervisorConfig struct {
	Bin         string
	Token       string
	MetricsAddr string
}

type TunnelToken struct {
	AccountID string `json:"a"`
	TunnelID  string `json:"t"`
	Secret    string `json:"s"`
}

func DecodeTunnelToken(raw string) (TunnelToken, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TunnelToken{}, errors.New("tunnel token is empty")
	}
	var decoded []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err = enc.DecodeString(raw); err == nil {
			break
		}
	}
	if err != nil {
		return TunnelToken{}, errors.New("tunnel token is not valid base64")
	}
	var tok TunnelToken
	if err := json.Unmarshal(decoded, &tok); err != nil {
		return TunnelToken{}, errors.New("tunnel token does not contain tunnel credentials")
	}
	if tok.AccountID == "" || tok.TunnelID == "" || tok.Secret == "" {
		return TunnelToken{}, errors.New("tunnel token is missing the account, tunnel or secret")
	}
	return tok, nil
}

func CheckLoopback(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("metrics address must be host:port: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("metrics address port %q is invalid", port)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("metrics address %s must be a loopback address such as %s", addr, DefaultMetricsAddr)
	}
	return nil
}
