package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func shouldConditionConnectionMutationsOnAuthorizedUserFields(t *testing.T, newStore func(*testing.T) store.DataStore) {
	data := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	orgID := store.NewID("org")
	if err := data.CreateOrg(ctx, &store.Org{ID: orgID, Name: orgID, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	record := &store.Connection{ID: store.NewID("conn"), OrgID: orgID, Type: "third", Name: "initial", Config: json.RawMessage("{}"), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := data.CreateConnection(ctx, record); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "config", "secrets", "type", "updated"} {
		expected, err := data.GetConnection(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		changed := *expected
		switch field {
		case "name":
			changed.Name = "owner-" + field
		case "config":
			changed.Config = json.RawMessage(`{"command":["true"]}`)
		case "secrets":
			changed.SecretsEnc = []byte("new-sealed")
		case "type":
			changed.Type = "other"
		case "updated":
			changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
		}
		if err := data.UpdateConnection(ctx, &changed); err != nil {
			t.Fatal(err)
		}
		proposal := *expected
		proposal.Name = "stripped"
		proposal.Config = json.RawMessage("{}")
		if err := data.UpdateConnectionIfCurrent(ctx, &proposal, expected); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("%s stale update: %v", field, err)
		}
		if err := data.DeleteConnectionIfCurrent(ctx, expected); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("%s stale delete: %v", field, err)
		}
		actual, err := data.GetConnection(ctx, record.ID)
		if err != nil || !store.SameConnectionInput(*actual, changed) {
			t.Fatalf("%s changed row lost: %+v %v", field, actual, err)
		}
	}
	expected, err := data.GetConnection(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateConnectionState(ctx, orgID, record.ID, json.RawMessage(`{"private":"new-state"}`)); err != nil {
		t.Fatal(err)
	}
	proposal := *expected
	proposal.Name = "updated-after-state"
	proposal.UpdatedAt = time.Now()
	if err := data.UpdateConnectionIfCurrent(ctx, &proposal, expected); err != nil {
		t.Fatalf("state-only update blocked: %v", err)
	}
	actual, err := data.GetConnection(ctx, record.ID)
	if err != nil || string(actual.State) != `{"private":"new-state"}` {
		t.Fatalf("state overwritten: %+v %v", actual, err)
	}
	foreign := *actual
	foreign.OrgID = "foreign"
	if err := data.DeleteConnectionIfCurrent(ctx, &foreign); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("foreign delete: %v", err)
	}
	if err := data.UpdateConnectionIfCurrent(ctx, &foreign, &foreign); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("foreign update: %v", err)
	}
	if err := data.UpdateConnectionState(ctx, orgID, record.ID, json.RawMessage(`{"private":"latest-state"}`)); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteConnectionIfCurrent(ctx, actual); err != nil {
		t.Fatalf("state-only write blocked delete: %v", err)
	}
}
