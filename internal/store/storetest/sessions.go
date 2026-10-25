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

	t.Run("rotate is a compare and swap on the token id", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		ts := sampleSession("tsn_cas", "org_1", "sa_1")
		_ = s.PutTokenSession(ctx, ts)
		first := *ts
		first.PrevTokenID, first.TokenID = ts.TokenID, "tid_first"
		if err := s.RotateTokenSession(ctx, &first, ts.TokenID); err != nil {
			t.Fatal(err)
		}
		loser := *ts
		loser.PrevTokenID, loser.TokenID = ts.TokenID, "tid_loser"
		if err := s.RotateTokenSession(ctx, &loser, ts.TokenID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("stale rotate err = %v", err)
		}
		if got, err := s.GetTokenSession(ctx, ts.ID); err != nil || got.TokenID != "tid_first" {
			t.Fatalf("got %+v err %v", got, err)
		}
		if err := s.DeleteTokenSession(ctx, ts.ID); err != nil {
			t.Fatal(err)
		}
		revived := first
		revived.PrevTokenID, revived.TokenID = first.TokenID, "tid_revived"
		if err := s.RotateTokenSession(ctx, &revived, first.TokenID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("rotate after delete err = %v", err)
		}
		if _, err := s.GetTokenSession(ctx, ts.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a rotate resurrected a deleted session: %v", err)
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

	t.Run("endpoint sessions are indexed and scoped", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		first := endpointSession("tsn_e1", "org_1", "ep_1", "usr_1", 0)
		second := endpointSession("tsn_e2", "org_1", "ep_1", "usr_2", time.Second)
		dropped := endpointSession("tsn_e3", "org_1", "ep_1", "usr_3", 2*time.Second)
		subjectRevoked := endpointSession("tsn_e4", "org_1", "ep_1", "usr_4", 3*time.Second)
		otherEndpoint := endpointSession("tsn_e5", "org_1", "ep_2", "usr_1", 0)
		otherOrg := endpointSession("tsn_e6", "org_2", "ep_1", "usr_1", 0)
		plain := sampleSession("tsn_e7", "org_1", "sa_1")
		for _, ts := range []*store.TokenSession{first, second, dropped, subjectRevoked, otherEndpoint, otherOrg, plain} {
			if err := s.PutTokenSession(ctx, ts); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteTokenSession(ctx, dropped.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeleteSubjectSessions(ctx, "org_1", store.SubjectUser, "usr_4"); err != nil {
			t.Fatal(err)
		}
		listed, err := s.ListEndpointSessions(ctx, "org_1", "ep_1")
		if err != nil {
			t.Fatal(err)
		}
		if got := sessionIDs(listed); !slices.Equal(got, []string{"tsn_e1", "tsn_e2"}) {
			t.Fatalf("listed %v, want the live ep_1 sessions oldest first", got)
		}
		if listed[0].SubjectID != "usr_1" || !listed[0].ExpiresAt.Equal(first.ExpiresAt) {
			t.Fatalf("listed session %+v lost its fields", listed[0])
		}
		deleted, err := s.DeleteEndpointSessions(ctx, "org_1", "ep_1")
		if err != nil {
			t.Fatal(err)
		}
		if got := sessionIDs(deleted); !slices.Equal(got, []string{"tsn_e1", "tsn_e2"}) {
			t.Fatalf("deleted %v", got)
		}
		for _, id := range []string{"tsn_e1", "tsn_e2"} {
			if _, err := s.GetTokenSession(ctx, id); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%s survived: %v", id, err)
			}
		}
		for _, id := range []string{"tsn_e5", "tsn_e6", "tsn_e7"} {
			if _, err := s.GetTokenSession(ctx, id); err != nil {
				t.Fatalf("%s wrongly deleted: %v", id, err)
			}
		}
		if left, err := s.ListEndpointSessions(ctx, "org_1", "ep_1"); err != nil || len(left) != 0 {
			t.Fatalf("after delete listed %v, err %v", sessionIDs(left), err)
		}
		if ids, err := s.DeleteSubjectSessions(ctx, "org_1", store.SubjectUser, "usr_1"); err != nil || !slices.Equal(ids, []string{"tsn_e5"}) {
			t.Fatalf("subject sessions after endpoint delete = %v, %v", ids, err)
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

func endpointSession(id, org, endpoint, user string, age time.Duration) *store.TokenSession {
	ts := sampleSession(id, org, user)
	ts.SubjectType = store.SubjectUser
	ts.EndpointID = endpoint
	ts.AuthMethod = "oidc"
	ts.CreatedAt = ts.CreatedAt.Add(age)
	return ts
}

func sessionIDs(sessions []store.TokenSession) []string {
	ids := make([]string, len(sessions))
	for i, ts := range sessions {
		ids[i] = ts.ID
	}
	return ids
}
