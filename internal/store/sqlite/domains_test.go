package sqlite

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
	for _, id := range []string{"org_a", "org_b"} {
		if err := s.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	a := &store.Domain{ID: "dom_a", OrgID: "org_a", Name: "app.example.com", Token: "ta", CreatedAt: now}
	b := &store.Domain{ID: "dom_b", OrgID: "org_b", Name: "app.example.com", Token: "tb", CreatedAt: now}
	if err := s.CreateDomain(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDomain(ctx, b); err != nil {
		t.Fatalf("unverified claims by different orgs may coexist: %v", err)
	}
	if err := s.CreateDomain(ctx, &store.Domain{ID: "dom_a2", OrgID: "org_a", Name: "app.example.com", Token: "x", CreatedAt: now}); err == nil {
		t.Fatal("same org registering the same name twice must fail")
	}
	if _, err := s.GetVerifiedDomain(ctx, "app.example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unverified domain must not be returned as verified: %v", err)
	}
	if err := s.SetDomainVerified(ctx, "dom_a", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomainVerified(ctx, "dom_b", now); err == nil {
		t.Fatal("a second org verifying the same name must fail")
	}
	got, err := s.GetVerifiedDomain(ctx, "app.example.com")
	if err != nil || got.OrgID != "org_a" || got.VerifiedAt == nil {
		t.Fatalf("verified lookup: %+v err=%v", got, err)
	}
	mine, err := s.GetDomain(ctx, "org_b", "app.example.com")
	if err != nil || mine.Token != "tb" || mine.VerifiedAt != nil {
		t.Fatalf("org-scoped get: %+v err=%v", mine, err)
	}
	list, err := s.ListDomainsByOrg(ctx, "org_a")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if err := s.DeleteDomain(ctx, "org_b", "dom_a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org delete must be not found: %v", err)
	}
	if err := s.DeleteDomain(ctx, "org_a", "dom_a"); err != nil {
		t.Fatal(err)
	}
}
