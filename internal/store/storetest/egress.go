package storetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func twoOrgs(t *testing.T, ds store.DataStore, now time.Time) (string, string) {
	t.Helper()
	org, other := store.NewID("org"), store.NewID("org")
	for _, id := range []string{org, other} {
		if err := ds.CreateOrg(context.Background(), &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return org, other
}

func RunEgressCredentials(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	ds := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	org, other := twoOrgs(t, ds, now)

	first := &store.EgressCredential{OrgID: org, Name: "billing", Header: "Authorization", Prefix: "Bearer ", Secret: "billing-key", AgentIDs: []string{"agt_a"}, UpdatedAt: now}
	if err := ds.PutEgressCredential(ctx, first); err != nil {
		t.Fatal(err)
	}
	if !first.CreatedAt.Equal(now) {
		t.Fatalf("first put = %+v", first)
	}
	later := now.Add(time.Minute)
	second := &store.EgressCredential{OrgID: org, Name: "billing", Header: "X-Api-Key", Secret: "billing-key-2", AgentIDs: []string{"agt_a", "agt_b"}, UpdatedAt: later}
	if err := ds.PutEgressCredential(ctx, second); err != nil {
		t.Fatal(err)
	}
	if !second.CreatedAt.Equal(now) {
		t.Fatalf("second put created = %v", second.CreatedAt)
	}
	got, err := ds.GetEgressCredential(ctx, org, "billing")
	if err != nil {
		t.Fatal(err)
	}
	if got.Header != "X-Api-Key" || got.Prefix != "" || got.Secret != "billing-key-2" || !slices.Equal(got.AgentIDs, []string{"agt_a", "agt_b"}) ||
		!got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(later) {
		t.Fatalf("get = %+v", got)
	}

	empty := &store.EgressCredential{OrgID: org, Name: "audit", Header: "X-Audit", Secret: "audit-key", UpdatedAt: now}
	if err := ds.PutEgressCredential(ctx, empty); err != nil {
		t.Fatal(err)
	}
	if got, err := ds.GetEgressCredential(ctx, org, "audit"); err != nil || got.AgentIDs == nil || len(got.AgentIDs) != 0 {
		t.Fatalf("empty agents = %+v, %v", got, err)
	}

	if _, err := ds.GetEgressCredential(ctx, other, "billing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org get err = %v", err)
	}
	if err := ds.DeleteEgressCredential(ctx, other, "billing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org delete err = %v", err)
	}
	if list, err := ds.ListEgressCredentials(ctx, other); err != nil || len(list) != 0 {
		t.Fatalf("other list = %v, %v", list, err)
	}
	list, err := ds.ListEgressCredentials(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "audit" || list[1].Name != "billing" {
		t.Fatalf("list = %+v", list)
	}

	if err := ds.DeleteEgressCredential(ctx, org, "billing"); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetEgressCredential(ctx, org, "billing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted get err = %v", err)
	}
	if err := ds.DeleteEgressCredential(ctx, org, "billing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func RunEgressDecrypt(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	ds := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	org, other := twoOrgs(t, ds, now)

	if _, err := ds.GetOrgCA(ctx, org); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing ca err = %v", err)
	}
	winner, err := ds.EnsureOrgCA(ctx, &store.OrgCA{OrgID: org, CertDER: []byte("cert-1"), KeyEnc: []byte("key-1"), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	loser, err := ds.EnsureOrgCA(ctx, &store.OrgCA{OrgID: org, CertDER: []byte("cert-2"), KeyEnc: []byte("key-2"), CreatedAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for _, ca := range []*store.OrgCA{winner, loser} {
		if !bytes.Equal(ca.CertDER, []byte("cert-1")) || !bytes.Equal(ca.KeyEnc, []byte("key-1")) || !ca.CreatedAt.Equal(now) {
			t.Fatalf("ensure ca = %+v", ca)
		}
	}
	if _, err := ds.GetOrgCA(ctx, other); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org ca err = %v", err)
	}

	if _, err := ds.GetOrgDecrypt(ctx, org); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing decrypt err = %v", err)
	}
	if err := ds.SetOrgDecrypt(ctx, &store.OrgDecrypt{OrgID: org, Allow: []string{"api.example.com", "*.corp.example"}, Bypass: []string{"bank.corp.example"}, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if err := ds.SetOrgDecrypt(ctx, &store.OrgDecrypt{OrgID: org, Allow: []string{"api.example.com"}, UpdatedAt: later}); err != nil {
		t.Fatal(err)
	}
	got, err := ds.GetOrgDecrypt(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Allow, []string{"api.example.com"}) || got.Bypass == nil || len(got.Bypass) != 0 || !got.UpdatedAt.Equal(later) {
		t.Fatalf("decrypt = %+v", got)
	}
	if _, err := ds.GetOrgDecrypt(ctx, other); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org decrypt err = %v", err)
	}
}
