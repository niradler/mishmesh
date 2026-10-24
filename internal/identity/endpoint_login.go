package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	EndpointCallbackPath = "/_mishmesh/auth/callback"
	EndpointLoginPath    = "/api/v1/auth/endpoint-login"
	EndpointSessionTTL   = 12 * time.Hour
	EndpointLoginTTL     = 60 * time.Second
	EndpointReauthMaxAge = 2 * time.Minute
	endpointLoginKey     = "eplogin:"
	endpointCodeBytes    = 32
	maxReturnPathBytes   = 2048
)

type EndpointLogin struct {
	EndpointID string           `json:"ep"`
	OrgID      string           `json:"org"`
	UserID     string           `json:"user"`
	AuthMethod authz.AuthMethod `json:"method"`
	AuthTime   time.Time        `json:"auth_time"`
	Return     string           `json:"return"`
}

func SafeReturnPath(p string) bool {
	if p == "" || len(p) > maxReturnPathBytes || p[0] != '/' {
		return false
	}
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return false
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return false
		}
	}
	return true
}

func (s *Service) PutEndpointLogin(ctx context.Context, l EndpointLogin) (string, error) {
	if l.EndpointID == "" || l.OrgID == "" || l.UserID == "" || !SafeReturnPath(l.Return) {
		return "", errors.New("identity: endpoint login needs endpoint, org, user and a local return path")
	}
	buf := make([]byte, endpointCodeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(buf)
	raw, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	if err := s.sessions.PutOnce(ctx, endpointLoginKey+store.HashToken(code), raw, EndpointLoginTTL); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return code, nil
}

func (s *Service) TakeEndpointLogin(ctx context.Context, code string) (EndpointLogin, error) {
	if code == "" || len(code) > 128 {
		return EndpointLogin{}, ErrUnauthenticated
	}
	raw, err := s.sessions.TakeOnce(ctx, endpointLoginKey+store.HashToken(code))
	if errors.Is(err, store.ErrNotFound) {
		return EndpointLogin{}, ErrUnauthenticated
	}
	if err != nil {
		return EndpointLogin{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var l EndpointLogin
	if err := json.Unmarshal(raw, &l); err != nil || !SafeReturnPath(l.Return) {
		return EndpointLogin{}, ErrUnauthenticated
	}
	return l, nil
}
