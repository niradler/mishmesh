package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestSessionStore(t *testing.T) {
	storetest.RunSessionStore(t, func(t *testing.T) store.SessionStore { return NewSessionStore() })
}

func TestSessionStoreExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newSessionStore(func() time.Time { return now })
	ctx := context.Background()
	_ = s.PutTokenSession(ctx, &store.TokenSession{ID: "tsn_1", OrgID: "o", SubjectType: store.SubjectUser, SubjectID: "u", TokenID: "t", ExpiresAt: now.Add(time.Minute)})
	_ = s.PutOnce(ctx, "code", []byte("x"), time.Minute)
	ok, _ := s.ClaimOnce(ctx, "jti", time.Minute)
	if !ok {
		t.Fatal("first claim failed")
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.GetTokenSession(ctx, "tsn_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session returned: %v", err)
	}
	if _, err := s.TakeOnce(ctx, "code"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired code returned: %v", err)
	}
	if ok, _ := s.ClaimOnce(ctx, "jti", time.Minute); !ok {
		t.Fatal("expired claim blocked a new claim")
	}
}
