package identity

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	ConsentTTL        = 5 * time.Minute
	consentKey        = "consent:"
	deviceDeniedKey   = "devdeny:"
	maxStateBytes     = 512
	consentDeniedCode = "access_denied"
)

var ErrAccessDenied = errors.New("identity: access denied")

type ConsentRequest struct {
	ID          string
	ClientID    string
	OrgID       string
	UserID      string
	RedirectURI string
	AuthMethod  authz.AuthMethod
	ExpiresAt   time.Time
}

type DeviceRequest struct {
	UserCode  string
	ClientID  string
	ExpiresAt time.Time
}

type pendingConsent struct {
	Login     AgentLogin `json:"login"`
	State     string     `json:"state"`
	ExpiresAt int64      `json:"exp"`
}

func ValidState(state string) bool {
	return state != "" && len(state) <= maxStateBytes
}

func (s *Service) StartConsent(ctx context.Context, l AgentLogin, state string) (string, error) {
	if l.OrgID == "" || l.UserID == "" || !ValidCodeChallenge(l.Challenge) || !LoopbackRedirect(l.RedirectURI) || !ValidState(state) {
		return "", errors.New("identity: consent needs org, user, S256 challenge, loopback redirect and state")
	}
	id, err := randomCode()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(pendingConsent{Login: l, State: state, ExpiresAt: s.now().Add(ConsentTTL).Unix()})
	if err != nil {
		return "", err
	}
	if err := s.sessions.PutOnce(ctx, consentKey+store.HashToken(id), raw, ConsentTTL); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return id, nil
}

func (s *Service) LookupConsent(ctx context.Context, id, userID string) (ConsentRequest, error) {
	pending, err := s.pendingConsent(ctx, id, userID)
	if err != nil {
		return ConsentRequest{}, err
	}
	return ConsentRequest{
		ID:          id,
		ClientID:    AgentClientID,
		OrgID:       pending.Login.OrgID,
		UserID:      pending.Login.UserID,
		RedirectURI: pending.Login.RedirectURI,
		AuthMethod:  pending.Login.AuthMethod,
		ExpiresAt:   time.Unix(pending.ExpiresAt, 0).UTC(),
	}, nil
}

func (s *Service) DecideConsent(ctx context.Context, id, userID string, approve bool) (string, ConsentRequest, error) {
	req, err := s.LookupConsent(ctx, id, userID)
	if err != nil {
		return "", ConsentRequest{}, err
	}
	raw, err := s.sessions.TakeOnce(ctx, consentKey+store.HashToken(id))
	if errors.Is(err, store.ErrNotFound) {
		return "", ConsentRequest{}, ErrUnauthenticated
	}
	if err != nil {
		return "", ConsentRequest{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var pending pendingConsent
	if err := json.Unmarshal(raw, &pending); err != nil || !s.ownsConsent(pending, userID) {
		return "", ConsentRequest{}, ErrUnauthenticated
	}
	v := url.Values{"state": {pending.State}}
	if !approve {
		v.Set("error", consentDeniedCode)
		return pending.Login.RedirectURI + "?" + v.Encode(), req, nil
	}
	code, err := s.PutAuthCode(ctx, pending.Login)
	if err != nil {
		return "", ConsentRequest{}, err
	}
	v.Set("code", code)
	return pending.Login.RedirectURI + "?" + v.Encode(), req, nil
}

func (s *Service) pendingConsent(ctx context.Context, id, userID string) (pendingConsent, error) {
	if id == "" || len(id) > maxCodeBytes || userID == "" {
		return pendingConsent{}, ErrUnauthenticated
	}
	raw, err := s.sessions.PeekOnce(ctx, consentKey+store.HashToken(id))
	if errors.Is(err, store.ErrNotFound) {
		return pendingConsent{}, ErrUnauthenticated
	}
	if err != nil {
		return pendingConsent{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var pending pendingConsent
	if err := json.Unmarshal(raw, &pending); err != nil || !s.ownsConsent(pending, userID) {
		return pendingConsent{}, ErrUnauthenticated
	}
	return pending, nil
}

func (s *Service) ownsConsent(p pendingConsent, userID string) bool {
	return subtle.ConstantTimeCompare([]byte(p.Login.UserID), []byte(userID)) == 1 && s.now().Before(time.Unix(p.ExpiresAt, 0))
}

func (s *Service) LookupDevice(ctx context.Context, userCode string) (DeviceRequest, error) {
	code := NormalizeUserCode(userCode)
	if code == "" {
		return DeviceRequest{}, ErrUnauthenticated
	}
	raw, err := s.sessions.PeekOnce(ctx, deviceUserKey+code)
	if errors.Is(err, store.ErrNotFound) {
		return DeviceRequest{}, ErrUnauthenticated
	}
	if err != nil {
		return DeviceRequest{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var pending pendingDevice
	if err := json.Unmarshal(raw, &pending); err != nil {
		return DeviceRequest{}, ErrUnauthenticated
	}
	expires := time.Unix(pending.ExpiresAt, 0)
	if !s.now().Before(expires) {
		return DeviceRequest{}, ErrUnauthenticated
	}
	return DeviceRequest{UserCode: code, ClientID: AgentClientID, ExpiresAt: expires.UTC()}, nil
}

func (s *Service) DenyDevice(ctx context.Context, userCode string) error {
	pending, remaining, err := s.takePendingDevice(ctx, userCode)
	if err != nil {
		return err
	}
	if err := s.sessions.PutOnce(ctx, deviceDeniedKey+pending.DeviceHash, []byte("1"), remaining); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (s *Service) takePendingDevice(ctx context.Context, userCode string) (pendingDevice, time.Duration, error) {
	code := NormalizeUserCode(userCode)
	if code == "" {
		return pendingDevice{}, 0, ErrUnauthenticated
	}
	raw, err := s.sessions.TakeOnce(ctx, deviceUserKey+code)
	if errors.Is(err, store.ErrNotFound) {
		return pendingDevice{}, 0, ErrUnauthenticated
	}
	if err != nil {
		return pendingDevice{}, 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var pending pendingDevice
	if err := json.Unmarshal(raw, &pending); err != nil {
		return pendingDevice{}, 0, ErrUnauthenticated
	}
	remaining := time.Unix(pending.ExpiresAt, 0).Sub(s.now())
	if remaining <= 0 {
		return pendingDevice{}, 0, ErrUnauthenticated
	}
	return pending, remaining, nil
}
