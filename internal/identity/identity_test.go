package identity

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/token"
)

func newService(t *testing.T) *Service {
	t.Helper()
	keys, err := token.NewKeyring(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(keys, memory.NewSessionStore())
}

var sa = authz.EntityRef{Type: authz.EntityServiceAccount, ID: "sa_1"}

func issue(t *testing.T, s *Service, org string) Issued {
	t.Helper()
	got, err := s.Issue(context.Background(), IssueRequest{OrgID: org, Subject: sa, Type: token.TypeAccess, AuthMethod: authz.AuthMethodClientCredentials, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAuthenticate(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	issued := issue(t, s, "org_1")
	p, err := s.Authenticate(ctx, issued.Token, token.TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if p.OrgID != "org_1" || p.Ref != sa || p.AuthMethod != authz.AuthMethodClientCredentials || p.SessionID != issued.Session.ID {
		t.Fatalf("principal = %+v", p)
	}
	if _, err := s.Authenticate(ctx, issued.Token, token.TypeSession); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong type accepted: %v", err)
	}
	if _, err := s.Authenticate(ctx, "garbage"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("garbage accepted: %v", err)
	}
}

func TestReissueInvalidatesOldToken(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	first := issue(t, s, "org_1")
	second, err := s.Reissue(ctx, first.Session.ID, token.TypeAccess, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, first.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old token still valid: %v", err)
	}
	if _, err := s.Authenticate(ctx, second.Token); err != nil {
		t.Fatalf("new token rejected: %v", err)
	}
}

func TestRevokeSubjectNotifies(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	a := issue(t, s, "org_1")
	b := issue(t, s, "org_1")
	other := issue(t, s, "org_2")
	var got []Revocation
	cancel := s.Subscribe(func(r Revocation) { got = append(got, r) })
	defer cancel()
	if err := s.RevokeSubject(ctx, "org_1", sa); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{a.Token, b.Token} {
		if _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("revoked token valid: %v", err)
		}
	}
	if _, err := s.Authenticate(ctx, other.Token); err != nil {
		t.Fatalf("other org token revoked: %v", err)
	}
	if len(got) != 1 || len(got[0].SessionIDs) != 2 || got[0].Subject != sa {
		t.Fatalf("revocations = %+v", got)
	}
}

func TestAuthenticateExpiredSession(t *testing.T) {
	s := newService(t)
	now := time.Unix(1_800_000_000, 0)
	s.SetClock(func() time.Time { return now })
	ctx := context.Background()
	issued := issue(t, s, "org_1")
	now = now.Add(2 * time.Hour)
	if _, err := s.Authenticate(ctx, issued.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired accepted: %v", err)
	}
}

type failingSessions struct{ store.SessionStore }

func (failingSessions) GetTokenSession(context.Context, string) (*store.TokenSession, error) {
	return nil, errors.New("down")
}

func TestAuthenticateStoreFailureIsUnavailable(t *testing.T) {
	s := newService(t)
	issued := issue(t, s, "org_1")
	s.sessions = failingSessions{s.sessions}
	if _, err := s.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestIssueRejectsBadSubjects(t *testing.T) {
	s := newService(t)
	for _, ref := range []authz.EntityRef{{Type: authz.EntityAgent, ID: "ag"}, {Type: authz.EntityUser}} {
		if _, err := s.Issue(context.Background(), IssueRequest{OrgID: "o", Subject: ref, Type: token.TypeAccess, TTL: time.Hour}); err == nil {
			t.Fatalf("issued for %+v", ref)
		}
	}
}
