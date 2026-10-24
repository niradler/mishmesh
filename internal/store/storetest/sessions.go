package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunSessionStore(t *testing.T, open func(t *testing.T) store.SessionStore) {
	t.Run("put get delete", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		ts := sampleSession("tsn_a", "org_1", "sa_1")
		if err := s.PutTokenSession(ctx, ts); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetTokenSession(ctx, ts.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.TokenID != ts.TokenID || got.SubjectID != ts.SubjectID || !got.ExpiresAt.Equal(ts.ExpiresAt) {
			t.Fatalf("got %+v", got)
		}
		if err := s.DeleteTokenSession(ctx, ts.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetTokenSession(ctx, ts.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after delete err = %v", err)
		}
		if err := s.DeleteTokenSession(ctx, "tsn_missing"); err != nil {
			t.Fatalf("delete missing: %v", err)
		}
	})

	t.Run("rotate replaces token id", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		ts := sampleSession("tsn_r", "org_1", "sa_1")
		_ = s.PutTokenSession(ctx, ts)
		rotatedAt := time.Now().UTC().Truncate(time.Second)
		ts.PrevTokenID, ts.TokenID, ts.RotatedAt = ts.TokenID, "tid_new", rotatedAt
		if err := s.PutTokenSession(ctx, ts); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetTokenSession(ctx, ts.ID)
		if err != nil || got.TokenID != "tid_new" || got.PrevTokenID != ts.PrevTokenID || !got.RotatedAt.Equal(rotatedAt) {
			t.Fatalf("got %+v err %v", got, err)
		}
	})

	t.Run("delete subject sessions is scoped", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		a := sampleSession("tsn_1", "org_1", "sa_1")
		b := sampleSession("tsn_2", "org_1", "sa_1")
		otherSubject := sampleSession("tsn_3", "org_1", "sa_2")
		otherOrg := sampleSession("tsn_4", "org_2", "sa_1")
		for _, ts := range []*store.TokenSession{a, b, otherSubject, otherOrg} {
			if err := s.PutTokenSession(ctx, ts); err != nil {
				t.Fatal(err)
			}
		}
		ids, err := s.DeleteSubjectSessions(ctx, "org_1", store.SubjectServiceAccount, "sa_1")
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"tsn_1", "tsn_2"}) {
			t.Fatalf("deleted %v", ids)
		}
		for _, id := range []string{"tsn_1", "tsn_2"} {
			if _, err := s.GetTokenSession(ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%s survived: %v", id, err)
			}
		}
		for _, id := range []string{"tsn_3", "tsn_4"} {
			if _, err := s.GetTokenSession(ctx, id); err != nil {
				t.Fatalf("%s wrongly deleted: %v", id, err)
			}
		}
	})

	t.Run("claim once", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		first, err := s.ClaimOnce(ctx, "jti:1", time.Minute)
		if err != nil || !first {
			t.Fatalf("first claim = %v %v", first, err)
		}
		again, err := s.ClaimOnce(ctx, "jti:1", time.Minute)
		if err != nil || again {
			t.Fatalf("replayed claim = %v %v", again, err)
		}
	})

	t.Run("take once", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		if err := s.PutOnce(ctx, "code:1", []byte("payload"), time.Minute); err != nil {
			t.Fatal(err)
		}
		got, err := s.TakeOnce(ctx, "code:1")
		if err != nil || string(got) != "payload" {
			t.Fatalf("take = %q %v", got, err)
		}
		if _, err := s.TakeOnce(ctx, "code:1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("second take err = %v", err)
		}
	})

	t.Run("peek once leaves the value", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		if _, err := s.PeekOnce(ctx, "consent:1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("peek missing err = %v", err)
		}
		if err := s.PutOnce(ctx, "consent:1", []byte("pending"), time.Minute); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			got, err := s.PeekOnce(ctx, "consent:1")
			if err != nil || string(got) != "pending" {
				t.Fatalf("peek = %q %v", got, err)
			}
		}
		got, err := s.TakeOnce(ctx, "consent:1")
		if err != nil || string(got) != "pending" {
			t.Fatalf("take after peek = %q %v", got, err)
		}
		if _, err := s.PeekOnce(ctx, "consent:1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("peek after take err = %v", err)
		}
	})
}

func sampleSession(id, org, subject string) *store.TokenSession {
	now := time.Now().UTC().Truncate(time.Second)
	return &store.TokenSession{
		ID:          id,
		OrgID:       org,
		SubjectType: store.SubjectServiceAccount,
		SubjectID:   subject,
		TokenID:     "tid_" + id,
		AuthMethod:  "client_credentials",
		AuthTime:    now,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Hour),
	}
}
