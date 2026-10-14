package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldUpdatePrivateConnectionStateWithoutOverwritingConfig(t *testing.T) {
	data := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	orgID := store.NewID("org")
	if err := data.CreateOrg(ctx, &store.Org{ID: orgID, Name: orgID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	record := &store.Connection{ID: store.NewID("conn"), OrgID: orgID, Type: "cloudflare", Name: "test", Config: json.RawMessage(`{"origin":"old"}`), CreatedAt: now, UpdatedAt: now}
	if err := data.CreateConnection(ctx, record); err != nil {
		t.Fatal(err)
	}
	stale, err := data.GetConnection(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"owned":["host.example.test"]}`)
	if err := data.UpdateConnectionState(ctx, orgID, record.ID, state); err != nil {
		t.Fatal(err)
	}
	stale.Config = json.RawMessage(`{"origin":"new"}`)
	if err := data.UpdateConnection(ctx, stale); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		got, err := data.GetConnection(ctx, record.ID)
		if err != nil || string(got.State) != string(state) || string(got.Config) != string(stale.Config) || !got.UpdatedAt.Equal(now) {
			t.Fatalf("state/config update=%+v err=%v", got, err)
		}
		if err := data.UpdateConnectionState(ctx, orgID, record.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.UpdateConnectionState(ctx, "foreign", record.ID, state); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign state write=%v", err)
	}
	if err := data.UpdateConnectionState(ctx, orgID, record.ID, json.RawMessage(`invalid-private`)); err == nil {
		t.Fatal("invalid private JSON accepted")
	}
	record.State = state
	raw, _ := json.Marshal(record)
	var public map[string]any
	if err := json.Unmarshal(raw, &public); err != nil {
		t.Fatal(err)
	}
	if _, exposed := public["State"]; exposed {
		t.Fatal("private state exposed in record JSON")
	}
	var writes sync.WaitGroup
	results := make(chan error, 2)
	writes.Add(2)
	go func() { defer writes.Done(); results <- data.UpdateConnection(ctx, stale) }()
	go func() { defer writes.Done(); results <- data.UpdateConnectionState(ctx, orgID, record.ID, state) }()
	writes.Wait()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	got, err := data.GetConnection(ctx, record.ID)
	if err != nil || string(got.State) != string(state) || string(got.Config) != string(stale.Config) {
		t.Fatalf("concurrent state/config writes lost data: %+v %v", got, err)
	}
}

func TestShouldMigratePrivateConnectionStateAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state-upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE orgs (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE connections (id TEXT PRIMARY KEY, org_id TEXT NOT NULL REFERENCES orgs(id), type TEXT NOT NULL, name TEXT NOT NULL, config TEXT NOT NULL, secrets_enc BLOB, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(org_id,name), UNIQUE(org_id,id));
INSERT INTO orgs VALUES ('org','test',1);
INSERT INTO connections VALUES ('conn','org','cloudflare','test','{}',NULL,1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for index := 0; index < 2; index++ {
		data, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		record, err := data.GetConnection(context.Background(), "conn")
		if err != nil || string(record.Config) != `{}` {
			t.Fatalf("migrated connection=%+v %v", record, err)
		}
		if index == 0 {
			if len(record.State) != 0 {
				t.Fatal("migration invented ownership")
			}
			if err := data.UpdateConnectionState(context.Background(), "org", "conn", json.RawMessage(`{"owned":true}`)); err != nil {
				t.Fatal(err)
			}
		} else if string(record.State) != `{"owned":true}` {
			t.Fatalf("private state lost on reopen: %s", record.State)
		}
		data.Close()
	}
}
