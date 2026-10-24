package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

const (
	AgentClientID       = "mishmesh-agent"
	AgentAccessTTL      = time.Hour
	AgentSessionTTL     = 7 * 24 * time.Hour
	RefreshGrace        = 20 * time.Second
	refreshGraceKey     = "rgrace:"
	AuthCodeTTL         = 60 * time.Second
	DeviceCodeTTL       = 10 * time.Minute
	DevicePollInterval  = 5 * time.Second
	authCodeKey         = "authcode:"
	deviceUserKey       = "devuser:"
	deviceApprovedKey   = "devok:"
	devicePollKey       = "devpoll:"
	userCodeAlphabet    = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLength      = 8
	minVerifierLength   = 43
	maxVerifierLength   = 128
	maxCodeBytes        = 128
	maxRedirectURIBytes = 512
)

var (
	ErrAuthorizationPending = errors.New("identity: authorization pending")
	ErrSlowDown             = errors.New("identity: polling too fast")
	ErrExpiredCode          = errors.New("identity: code expired")
)

type AgentLogin struct {
	OrgID       string           `json:"org"`
	UserID      string           `json:"user"`
	AuthMethod  authz.AuthMethod `json:"method"`
	AuthTime    time.Time        `json:"auth_time"`
	Challenge   string           `json:"challenge,omitempty"`
	RedirectURI string           `json:"redirect_uri,omitempty"`
}

type AgentTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
	Session      *store.TokenSession
}

type DeviceStart struct {
	DeviceCode string
	UserCode   string
	ExpiresIn  time.Duration
	Interval   time.Duration
}

func LoopbackRedirect(raw string) bool {
	if raw == "" || len(raw) > maxRedirectURIBytes {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.Port() == "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

func ValidCodeChallenge(challenge string) bool {
	if len(challenge) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil
}

func validVerifier(verifier string) bool {
	if len(verifier) < minVerifierLength || len(verifier) > maxVerifierLength {
		return false
	}
	for _, c := range verifier {
		unreserved := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)
		if !unreserved {
			return false
		}
	}
	return true
}

func VerifyPKCE(challenge, verifier string) bool {
	if !validVerifier(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func randomCode() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *Service) PutAuthCode(ctx context.Context, l AgentLogin) (string, error) {
	if l.OrgID == "" || l.UserID == "" || !ValidCodeChallenge(l.Challenge) || !LoopbackRedirect(l.RedirectURI) {
		return "", errors.New("identity: auth code needs org, user, S256 challenge and loopback redirect")
	}
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	if err := s.sessions.PutOnce(ctx, authCodeKey+store.HashToken(code), raw, AuthCodeTTL); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return code, nil
}

func (s *Service) RedeemAuthCode(ctx context.Context, code, verifier, redirectURI string) (AgentLogin, error) {
	if code == "" || len(code) > maxCodeBytes {
		return AgentLogin{}, ErrUnauthenticated
	}
	raw, err := s.sessions.TakeOnce(ctx, authCodeKey+store.HashToken(code))
	if errors.Is(err, store.ErrNotFound) {
		return AgentLogin{}, ErrUnauthenticated
	}
	if err != nil {
		return AgentLogin{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var l AgentLogin
	if err := json.Unmarshal(raw, &l); err != nil {
		return AgentLogin{}, ErrUnauthenticated
	}
	if l.RedirectURI != redirectURI || !VerifyPKCE(l.Challenge, verifier) {
		return AgentLogin{}, ErrUnauthenticated
	}
	return l, nil
}

func newUserCode() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	for i := range userCodeLength {
		if i == userCodeLength/2 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func NormalizeUserCode(raw string) string {
	if len(raw) > maxCodeBytes {
		return ""
	}
	var b strings.Builder
	for _, c := range strings.ToUpper(raw) {
		if strings.ContainsRune(userCodeAlphabet, c) {
			b.WriteRune(c)
		} else if c != '-' && c != ' ' {
			return ""
		}
	}
	code := b.String()
	if len(code) != userCodeLength {
		return ""
	}
	return code[:userCodeLength/2] + "-" + code[userCodeLength/2:]
}

type pendingDevice struct {
	DeviceHash string `json:"device"`
	ExpiresAt  int64  `json:"exp"`
}

func (s *Service) StartDevice(ctx context.Context) (DeviceStart, error) {
	secret, err := randomCode()
	if err != nil {
		return DeviceStart{}, err
	}
	userCode, err := newUserCode()
	if err != nil {
		return DeviceStart{}, err
	}
	expires := s.now().Add(DeviceCodeTTL)
	deviceCode := strconv.FormatInt(expires.Unix(), 10) + "." + secret
	raw, err := json.Marshal(pendingDevice{DeviceHash: store.HashToken(deviceCode), ExpiresAt: expires.Unix()})
	if err != nil {
		return DeviceStart{}, err
	}
	if err := s.sessions.PutOnce(ctx, deviceUserKey+userCode, raw, DeviceCodeTTL); err != nil {
		return DeviceStart{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return DeviceStart{DeviceCode: deviceCode, UserCode: userCode, ExpiresIn: DeviceCodeTTL, Interval: DevicePollInterval}, nil
}

func (s *Service) ApproveDevice(ctx context.Context, userCode string, l AgentLogin) error {
	if l.OrgID == "" || l.UserID == "" {
		return ErrUnauthenticated
	}
	pending, remaining, err := s.takePendingDevice(ctx, userCode)
	if err != nil {
		return err
	}
	l.Challenge, l.RedirectURI = "", ""
	login, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := s.sessions.PutOnce(ctx, deviceApprovedKey+pending.DeviceHash, login, remaining); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (s *Service) PollDevice(ctx context.Context, deviceCode string) (AgentLogin, error) {
	expRaw, _, ok := strings.Cut(deviceCode, ".")
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if !ok || err != nil || len(deviceCode) > maxCodeBytes {
		return AgentLogin{}, ErrUnauthenticated
	}
	if !s.now().Before(time.Unix(exp, 0)) {
		return AgentLogin{}, ErrExpiredCode
	}
	hash := store.HashToken(deviceCode)
	fresh, err := s.sessions.ClaimOnce(ctx, devicePollKey+hash, DevicePollInterval-time.Second)
	if err != nil {
		return AgentLogin{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !fresh {
		return AgentLogin{}, ErrSlowDown
	}
	raw, err := s.sessions.TakeOnce(ctx, deviceApprovedKey+hash)
	if errors.Is(err, store.ErrNotFound) {
		return AgentLogin{}, s.pendingOrDenied(ctx, hash)
	}
	if err != nil {
		return AgentLogin{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var l AgentLogin
	if err := json.Unmarshal(raw, &l); err != nil {
		return AgentLogin{}, ErrUnauthenticated
	}
	return l, nil
}

func (s *Service) pendingOrDenied(ctx context.Context, hash string) error {
	_, err := s.sessions.PeekOnce(ctx, deviceDeniedKey+hash)
	switch {
	case err == nil:
		return ErrAccessDenied
	case errors.Is(err, store.ErrNotFound):
		return ErrAuthorizationPending
	default:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

func (s *Service) IssueAgentUser(ctx context.Context, l AgentLogin) (AgentTokens, error) {
	if l.OrgID == "" || l.UserID == "" {
		return AgentTokens{}, errors.New("identity: agent user session needs org and user")
	}
	now := s.now().UTC()
	authTime := l.AuthTime
	if authTime.IsZero() {
		authTime = now
	}
	ts := &store.TokenSession{
		ID:          store.NewID("tsn"),
		OrgID:       l.OrgID,
		SubjectType: store.SubjectUser,
		SubjectID:   l.UserID,
		TokenID:     store.NewID("tid"),
		AuthMethod:  string(l.AuthMethod),
		AuthTime:    authTime.UTC(),
		CreatedAt:   now,
		ExpiresAt:   now.Add(AgentSessionTTL),
	}
	return s.storeAgentTokens(ctx, ts)
}

func (s *Service) RefreshAgentUser(ctx context.Context, refresh string) (AgentTokens, error) {
	ts, _, err := s.verify(ctx, refresh, []token.Type{token.TypeRefresh})
	if errors.Is(err, errTokenSuperseded) {
		return s.refreshWithinGrace(ctx, ts, refresh)
	}
	if err != nil {
		return AgentTokens{}, err
	}
	if ts.SubjectType != store.SubjectUser {
		return AgentTokens{}, ErrUnauthenticated
	}
	now := s.now().UTC()
	prev := ts.TokenID
	ts.PrevTokenID, ts.TokenID, ts.RotatedAt = prev, nextTokenID(ts.ID, prev), now
	ts.ExpiresAt = now.Add(AgentSessionTTL)
	tokens, err := s.signAgentTokens(ts)
	if err != nil {
		return AgentTokens{}, err
	}
	err = s.sessions.RotateTokenSession(ctx, ts, prev)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return s.RefreshAgentUser(ctx, refresh)
	case err != nil:
		return AgentTokens{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return tokens, nil
}

func (s *Service) refreshWithinGrace(ctx context.Context, ts *store.TokenSession, refresh string) (AgentTokens, error) {
	claims, err := s.keys.Verify(refresh, s.now())
	if err != nil {
		return AgentTokens{}, ErrUnauthenticated
	}
	now := s.now()
	inGrace := ts.SubjectType == store.SubjectUser &&
		ts.PrevTokenID != "" &&
		subtle.ConstantTimeCompare([]byte(ts.PrevTokenID), []byte(claims.TokenID)) == 1 &&
		now.Before(ts.RotatedAt.Add(RefreshGrace)) &&
		now.Before(ts.ExpiresAt)
	if inGrace {
		first, err := s.sessions.ClaimOnce(ctx, refreshGraceKey+ts.ID+":"+claims.TokenID, RefreshGrace)
		if err != nil {
			return AgentTokens{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		if first {
			return s.signAgentTokens(ts)
		}
	}
	if err := s.RevokeSession(ctx, ts.ID); err != nil {
		return AgentTokens{}, err
	}
	return AgentTokens{}, ErrRefreshReused
}

func nextTokenID(sessionID, tokenID string) string {
	sum := sha256.Sum256([]byte(sessionID + "|" + tokenID))
	return "tid_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:15]))
}

func (s *Service) RevokeToken(ctx context.Context, raw string) error {
	ts, _, err := s.verify(ctx, raw, []token.Type{token.TypeAgentUser, token.TypeRefresh})
	if errors.Is(err, ErrUnauthenticated) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.RevokeSession(ctx, ts.ID)
}

func (s *Service) storeAgentTokens(ctx context.Context, ts *store.TokenSession) (AgentTokens, error) {
	tokens, err := s.signAgentTokens(ts)
	if err != nil {
		return AgentTokens{}, err
	}
	if err := s.sessions.PutTokenSession(ctx, ts); err != nil {
		return AgentTokens{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return tokens, nil
}

func (s *Service) signAgentTokens(ts *store.TokenSession) (AgentTokens, error) {
	now := s.now()
	accessExp := now.Add(AgentAccessTTL)
	if accessExp.After(ts.ExpiresAt) {
		accessExp = ts.ExpiresAt
	}
	access, err := s.signUntil(ts, token.TypeAgentUser, accessExp)
	if err != nil {
		return AgentTokens{}, err
	}
	refresh, err := s.signUntil(ts, token.TypeRefresh, ts.ExpiresAt)
	if err != nil {
		return AgentTokens{}, err
	}
	return AgentTokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: accessExp.Sub(now).Round(time.Second), Session: ts}, nil
}

func (s *Service) signUntil(ts *store.TokenSession, typ token.Type, exp time.Time) (string, error) {
	return s.keys.Sign(token.Claims{
		Type:      typ,
		SessionID: ts.ID,
		TokenID:   ts.TokenID,
		OrgID:     ts.OrgID,
		IssuedAt:  s.now().Unix(),
		ExpiresAt: exp.Unix(),
	})
}
