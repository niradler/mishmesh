package agent

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func NormalizeGatewayURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("gateway URL is empty (set gateway in the config, --gateway, or MISHMESH_GATEWAY_URL)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid gateway URL %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "":
		return "", fmt.Errorf("invalid gateway URL %q: missing scheme (use ws://, wss://, http:// or https://)", raw)
	default:
		return "", fmt.Errorf("invalid gateway URL %q: unsupported scheme %q (use ws, wss, http or https)", raw, u.Scheme)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Hostname() == "" {
		return "", fmt.Errorf("invalid gateway URL %q: missing host", raw)
	}
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}
