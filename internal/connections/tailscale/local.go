package tailscaleconnection

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/paths"
)

type LocalAPI interface {
	StatusWithoutPeers(context.Context) (*ipnstate.Status, error)
	GetServeConfig(context.Context) (*ipn.ServeConfig, error)
	SetServeConfig(context.Context, *ipn.ServeConfig) error
	GetPrefs(context.Context) (*ipn.Prefs, error)
	EditPrefs(context.Context, *ipn.MaskedPrefs) (*ipn.Prefs, error)
}

func NewLocalAPI(socket string, transport http.RoundTripper) LocalAPI {
	return &local.Client{Socket: socket, UseSocketOnly: true, Transport: transport}
}

func canonicalSocket(socket string) (string, error) {
	if socket == "" {
		socket = paths.DefaultTailscaledSocket()
	}
	if strings.ContainsRune(socket, 0) || len(socket) > 4096 {
		return "", errors.New("invalid Tailscale socket")
	}
	if strings.HasPrefix(socket, `\\.\pipe\`) {
		return strings.ToLower(socket), nil
	}
	if !filepath.IsAbs(socket) {
		return "", errors.New("Tailscale socket must be absolute")
	}
	if resolved, err := filepath.EvalSymlinks(socket); err == nil {
		socket = resolved
	}
	return filepath.Clean(socket), nil
}

func isETagConflict(err error) bool {
	return local.IsPreconditionsFailedError(err)
}
