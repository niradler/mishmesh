package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunWorkloadIdentities(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	ds := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	org, other := store.NewID("org"), store.NewID("org")
	for _, id := range []string{org, other} {
		if err := ds.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	sa := &store.ServiceAccount{ID: store.NewID("sa"), OrgID: org, Name: "ci", Role: "member", ClientID: store.NewID("cid"), SecretHash: "h", CreatedAt: now, UpdatedAt: now}
	foreignSA := &store.ServiceAccount{ID: store.NewID("sa"), OrgID: other, Name: "ci", Role: "member", ClientID: store.NewID("cid"), SecretHash: "h", CreatedAt: now, UpdatedAt: now}
	for _, s := range []*store.ServiceAccount{sa, foreignSA} {
		if err := ds.CreateServiceAccount(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	const issuer = "https://issuer.example"
	w := &store.WorkloadIdentity{ID: store.NewID("wid"), OrgID: org, ServiceAccountID: sa.ID, Issuer: issuer, Audience: "mishmesh", Subject: "repo:a", CreatedAt: now}
	if err := ds.CreateWorkloadIdentity(ctx, w); err != nil {
		t.Fatal(err)
	}
	dup := *w
	dup.ID = store.NewID("wid")
	if err := ds.CreateWorkloadIdentity(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate err = %v, want conflict", err)
	}
	crossOrg := &store.WorkloadIdentity{ID: store.NewID("wid"), OrgID: other, ServiceAccountID: sa.ID, Issuer: issuer, Audience: "x", Subject: "repo:b", CreatedAt: now}
	if err := ds.CreateWorkloadIdentity(ctx, crossOrg); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org create err = %v, want not found", err)
	}
	foreign := &store.WorkloadIdentity{ID: store.NewID("wid"), OrgID: other, ServiceAccountID: foreignSA.ID, Issuer: issuer, Audience: "mishmesh", Subject: "repo:a", CreatedAt: now}
	if err := ds.CreateWorkloadIdentity(ctx, foreign); err != nil {
		t.Fatal(err)
	}

	list, err := ds.ListWorkloadIdentities(ctx, org, sa.ID)
	if err != nil || len(list) != 1 || *list[0] != *w {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list, err := ds.ListWorkloadIdentities(ctx, other, sa.ID); err != nil || len(list) != 0 {
		t.Fatalf("cross-org list = %+v, %v", list, err)
	}
	byIssuer, err := ds.ListWorkloadIdentitiesByIssuer(ctx, issuer)
	if err != nil || len(byIssuer) != 2 {
		t.Fatalf("by issuer = %+v, %v", byIssuer, err)
	}
	if none, err := ds.ListWorkloadIdentitiesByIssuer(ctx, issuer+"/"); err != nil || len(none) != 0 {
		t.Fatalf("issuer must match exactly: %+v, %v", none, err)
	}

	if err := ds.DeleteWorkloadIdentity(ctx, other, sa.ID, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org delete err = %v", err)
	}
	if err := ds.DeleteServiceAccount(ctx, other, foreignSA.ID); err != nil {
		t.Fatal(err)
	}
	if byIssuer, err := ds.ListWorkloadIdentitiesByIssuer(ctx, issuer); err != nil || len(byIssuer) != 1 || byIssuer[0].ID != w.ID {
		t.Fatalf("service account delete did not cascade: %+v, %v", byIssuer, err)
	}
	if err := ds.DeleteWorkloadIdentity(ctx, org, sa.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := ds.DeleteWorkloadIdentity(ctx, org, sa.ID, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("double delete err = %v", err)
	}
}
