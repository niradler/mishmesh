package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

var (
	ErrUnauthenticated = errors.New("identity: unauthenticated")
	ErrUnavailable     = errors.New("identity: session store unavailable")
)

type Scope struct {
	OrgID          string
	AgentID        string
	SessionIDHash  string
	TokenSessionID string
}

type Deciders interface {
	Decider(scope Scope) authz.Decider
}

type Principal struct {
	OrgID      string
	Ref        authz.EntityRef
	SessionID  string
	Type       token.Type
	AuthMethod authz.AuthMethod
	AuthTime   time.Time
	ExpiresAt  time.Time
	EndpointID string
	AgentID    string
}

func (p Principal) Scope(agentID string) Scope {
	return Scope{OrgID: p.OrgID, AgentID: agentID, TokenSessionID: p.SessionID}
}

type Revocation struct {
	OrgID      string
	Subject    authz.EntityRef
	SessionIDs []string
}

type IssueRequest struct {
	SessionID  string
	OrgID      string
	Subject    authz.EntityRef
	Type       token.Type
	AuthMethod authz.AuthMethod
	AuthTime   time.Time
	TTL        time.Duration
	EndpointID string
	AgentID    string
}

type Issued struct {
	Token     string
	Session   *store.TokenSession
	ExpiresIn time.Duration
}

type Service struct {
	keys     *token.Keyring
	sessions store.SessionStore
	now      func() time.Time

	mu          sync.Mutex
	subscribers map[int]func(Revocation)
	nextSub     int
}

func New(keys *token.Keyring, sessions store.SessionStore) *Service {
	return &Service{keys: keys, sessions: sessions, now: time.Now, subscribers: make(map[int]func(Revocation))}
}

func (s *Service) SetClock(now func() time.Time) { s.now = now }

func (s *Service) Sessions() store.SessionStore { return s.sessions }

func SubjectType(t authz.EntityType) (string, bool) {
	switch t {
	case authz.EntityUser:
		return store.SubjectUser, true
	case authz.EntityServiceAccount:
		return store.SubjectServiceAccount, true
	}
	return "", false
}

func EntityType(subjectType string) (authz.EntityType, bool) {
	switch subjectType {
	case store.SubjectUser:
		return authz.EntityUser, true
	case store.SubjectServiceAccount:
		return authz.EntityServiceAccount, true
	}
	return "", false
}

func (s *Service) Issue(ctx context.Context, req IssueRequest) (Issued, error) {
	subjectType, ok := SubjectType(req.Subject.Type)
	if !ok || req.Subject.ID == "" || req.OrgID == "" || req.TTL <= 0 {
		return Issued{}, errors.New("identity: issue needs org, user or service account subject and ttl")
	}
	now := s.now().UTC()
	authTime := req.AuthTime
	if authTime.IsZero() {
		authTime = now
	}
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = store.NewID("tsn")
	}
	ts := &store.TokenSession{
		ID:          sessionID,
		OrgID:       req.OrgID,
		SubjectType: subjectType,
		SubjectID:   req.Subject.ID,
		TokenID:     store.NewID("tid"),
		AuthMethod:  string(req.AuthMethod),
		AuthTime:    authTime.UTC(),
		CreatedAt:   now,
		ExpiresAt:   now.Add(req.TTL),
		EndpointID:  req.EndpointID,
		AgentID:     req.AgentID,
	}
	return s.store(ctx, ts, req.Type)
}

func (s *Service) Reissue(ctx context.Context, sessionID string, typ token.Type, ttl time.Duration) (Issued, error) {
	ts, err := s.sessions.GetTokenSession(ctx, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return Issued{}, ErrUnauthenticated
	}
	if err != nil {
		return Issued{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	now := s.now().UTC()
	ts.TokenID = store.NewID("tid")
	ts.ExpiresAt = now.Add(ttl)
	return s.store(ctx, ts, typ)
}

func (s *Service) Resign(ctx context.Context, ts *store.TokenSession, typ token.Type) (Issued, error) {
	raw, err := s.sign(ts, typ)
	if err != nil {
		return Issued{}, err
	}
	return Issued{Token: raw, Session: ts, ExpiresIn: ts.ExpiresAt.Sub(s.now()).Round(time.Second)}, nil
}

func (s *Service) store(ctx context.Context, ts *store.TokenSession, typ token.Type) (Issued, error) {
	raw, err := s.sign(ts, typ)
	if err != nil {
		return Issued{}, err
	}
	if err := s.sessions.PutTokenSession(ctx, ts); err != nil {
		return Issued{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return Issued{Token: raw, Session: ts, ExpiresIn: ts.ExpiresAt.Sub(s.now()).Round(time.Second)}, nil
}

func (s *Service) sign(ts *store.TokenSession, typ token.Type) (string, error) {
	return s.keys.Sign(token.Claims{
		Type:      typ,
		SessionID: ts.ID,
		TokenID:   ts.TokenID,
		OrgID:     ts.OrgID,
		IssuedAt:  s.now().Unix(),
		ExpiresAt: ts.ExpiresAt.Unix(),
	})
}

func (s *Service) verify(ctx context.Context, raw string, allowed []token.Type) (*store.TokenSession, token.Type, error) {
	claims, err := s.keys.Verify(raw, s.now())
	if err != nil {
		return nil, "", ErrUnauthenticated
	}
	if len(allowed) > 0 && !slices.Contains(allowed, claims.Type) {
		return nil, "", ErrUnauthenticated
	}
	ts, err := s.sessions.GetTokenSession(ctx, claims.SessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", ErrUnauthenticated
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if subtle.ConstantTimeCompare([]byte(ts.TokenID), []byte(claims.TokenID)) != 1 || ts.OrgID != claims.OrgID {
		return nil, "", ErrUnauthenticated
	}
	if !s.now().Before(ts.ExpiresAt) {
		return nil, "", ErrUnauthenticated
	}
	return ts, claims.Type, nil
}

func (s *Service) Authenticate(ctx context.Context, raw string, allowed ...token.Type) (Principal, error) {
	ts, typ, err := s.verify(ctx, raw, allowed)
	if err != nil {
		return Principal{}, err
	}
	entity, ok := EntityType(ts.SubjectType)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{
		OrgID:      ts.OrgID,
		Ref:        authz.EntityRef{Type: entity, ID: ts.SubjectID},
		SessionID:  ts.ID,
		Type:       typ,
		AuthMethod: authz.AuthMethod(ts.AuthMethod),
		AuthTime:   ts.AuthTime,
		ExpiresAt:  ts.ExpiresAt,
		EndpointID: ts.EndpointID,
		AgentID:    ts.AgentID,
	}, nil
}

func (s *Service) IssueGuest(ctx context.Context, orgID, endpointID, email string, ttl time.Duration) (Issued, error) {
	if orgID == "" || endpointID == "" || email == "" || ttl <= 0 {
		return Issued{}, errors.New("identity: guest session needs org, endpoint, email and ttl")
	}
	now := s.now().UTC()
	ts := &store.TokenSession{
		ID:          store.NewID("tsn"),
		OrgID:       orgID,
		SubjectType: store.SubjectGuest,
		SubjectID:   email,
		TokenID:     store.NewID("tid"),
		AuthMethod:  string(authz.AuthMethodOIDC),
		AuthTime:    now,
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
		EndpointID:  endpointID,
	}
	return s.store(ctx, ts, token.TypeSession)
}

func (s *Service) AuthenticateGuest(ctx context.Context, raw, endpointID string) (string, error) {
	ts, _, err := s.verify(ctx, raw, []token.Type{token.TypeSession})
	if err != nil {
		return "", err
	}
	if ts.SubjectType != store.SubjectGuest || ts.EndpointID != endpointID || ts.SubjectID == "" {
		return "", ErrUnauthenticated
	}
	return ts.SubjectID, nil
}

func (s *Service) RevokeSession(ctx context.Context, id string) error {
	ts, err := s.sessions.GetTokenSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := s.sessions.DeleteTokenSession(ctx, id); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	entity, _ := EntityType(ts.SubjectType)
	s.publish(Revocation{OrgID: ts.OrgID, Subject: authz.EntityRef{Type: entity, ID: ts.SubjectID}, SessionIDs: []string{id}})
	return nil
}

func (s *Service) RevokeSubject(ctx context.Context, orgID string, subject authz.EntityRef) error {
	subjectType, ok := SubjectType(subject.Type)
	if !ok {
		return errors.New("identity: revoke needs a user or service account")
	}
	ids, err := s.sessions.DeleteSubjectSessions(ctx, orgID, subjectType, subject.ID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.publish(Revocation{OrgID: orgID, Subject: subject, SessionIDs: ids})
	return nil
}

func (s *Service) Subscribe(fn func(Revocation)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	s.subscribers[id] = fn
	return func() {
		s.mu.Lock()
		delete(s.subscribers, id)
		s.mu.Unlock()
	}
}

func (s *Service) publish(rev Revocation) {
	s.mu.Lock()
	subs := make([]func(Revocation), 0, len(s.subscribers))
	for _, fn := range s.subscribers {
		subs = append(subs, fn)
	}
	s.mu.Unlock()
	for _, fn := range subs {
		fn(rev)
	}
}
