package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestDomainVerificationUniqueness(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	orgA, orgB := store.NewID("org"), store.NewID("org")
	for _, id := range []string{orgA, orgB} {
		if err := s.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	name := store.NewID("app") + ".example.com"
	a := &store.Domain{ID: store.NewID("dom"), OrgID: orgA, Name: name, Token: "ta", CreatedAt: now}
	b := &store.Domain{ID: store.NewID("dom"), OrgID: orgB, Name: name, Token: "tb", CreatedAt: now}
	if err := s.CreateDomain(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDomain(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVerifiedDomain(ctx, name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unverified domain must not be returned as verified: %v", err)
	}
	if err := s.SetDomainVerified(ctx, a.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomainVerified(ctx, b.ID, now); err == nil {
		t.Fatal("a second org verifying the same name must fail")
	}
	got, err := s.GetVerifiedDomain(ctx, name)
	if err != nil || got.OrgID != orgA || got.VerifiedAt == nil {
		t.Fatalf("verified lookup: %+v err=%v", got, err)
	}
	list, err := s.ListDomainsByOrg(ctx, orgA)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if err := s.DeleteDomain(ctx, orgB, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org delete must be not found: %v", err)
	}
	if err := s.DeleteDomain(ctx, orgA, a.ID); err != nil {
		t.Fatal(err)
	}
}
