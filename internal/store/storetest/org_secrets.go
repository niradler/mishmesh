package storetest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunOrgSecrets(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	ds := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	org, other := store.NewID("org"), store.NewID("org")
	for _, id := range []string{org, other} {
		if err := ds.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	first := &store.OrgSecret{OrgID: org, Name: "api-key", ValueEnc: []byte("v1"), UpdatedAt: now}
	if err := ds.PutOrgSecret(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || !first.CreatedAt.Equal(now) {
		t.Fatalf("first put = %+v", first)
	}
	later := now.Add(time.Minute)
	second := &store.OrgSecret{OrgID: org, Name: "api-key", ValueEnc: []byte("v2"), UpdatedAt: later}
	if err := ds.PutOrgSecret(ctx, second); err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 || !second.CreatedAt.Equal(now) {
		t.Fatalf("second put = %+v", second)
	}
	got, err := ds.GetOrgSecret(ctx, org, "api-key")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.ValueEnc, []byte("v2")) || got.Version != 2 || !got.UpdatedAt.Equal(later) || !got.CreatedAt.Equal(now) {
		t.Fatalf("get = %+v", got)
	}

	if _, err := ds.GetOrgSecret(ctx, other, "api-key"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org get err = %v", err)
	}
	if err := ds.DeleteOrgSecret(ctx, other, "api-key"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org delete err = %v", err)
	}
	if err := ds.PutOrgSecret(ctx, &store.OrgSecret{OrgID: other, Name: "api-key", ValueEnc: []byte("o"), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := ds.PutOrgSecret(ctx, &store.OrgSecret{OrgID: org, Name: "db", ValueEnc: []byte("d"), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	list, err := ds.ListOrgSecrets(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "api-key" || list[1].Name != "db" {
		t.Fatalf("list = %+v", list)
	}

	if err := ds.DeleteOrgSecret(ctx, org, "api-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.GetOrgSecret(ctx, org, "api-key"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted get err = %v", err)
	}
	if _, err := ds.GetOrgSecret(ctx, other, "api-key"); err != nil {
		t.Fatalf("other org secret gone: %v", err)
	}
	if err := ds.DeleteOrgSecret(ctx, org, "api-key"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("double delete err = %v", err)
	}
}
