package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestShouldSerializeOpposingRequirementUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requirements.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	orgID := store.NewID("org")
	if err := first.CreateOrg(ctx, &store.Org{ID: orgID, Name: orgID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		a := &store.Service{ID: store.NewID("svc"), OrgID: orgID, Name: store.NewID("alpha"), Driver: "custom", Config: json.RawMessage("{}"), CreatedAt: now, UpdatedAt: now}
		b := &store.Service{ID: store.NewID("svc"), OrgID: orgID, Name: store.NewID("beta"), Driver: "custom", Config: json.RawMessage("{}"), CreatedAt: now, UpdatedAt: now}
		for _, svc := range []*store.Service{a, b} {
			if err := first.CreateService(ctx, svc); err != nil {
				t.Fatal(err)
			}
		}
		a.Requires, b.Requires = []string{b.ID}, []string{a.ID}
		start, results := make(chan struct{}), make(chan error, 2)
		go func() { <-start; results <- first.UpdateService(ctx, a) }()
		go func() { <-start; results <- second.UpdateService(ctx, b) }()
		close(start)
		successes, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				successes++
			} else if errors.Is(err, store.ErrConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected concurrent error: %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("opposing updates: %d successes, %d conflicts", successes, conflicts)
		}
		graph, err := first.ListServices(ctx, orgID)
		if err != nil {
			t.Fatal(err)
		}
		for _, svc := range graph {
			if err := store.ValidateRequirements(orgID, svc.ID, svc.Requires, graph); err != nil {
				t.Fatalf("persisted cycle: %v", err)
			}
		}
	}
}

func TestShouldRejectLegacyEncryptedSchemaWithoutMatchingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE orgs (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE services (id TEXT PRIMARY KEY, org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE, name TEXT NOT NULL, driver TEXT NOT NULL, spec TEXT NOT NULL, idle_timeout_seconds INTEGER NOT NULL DEFAULT 0, start_timeout_seconds INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(org_id, name));
CREATE TABLE cloudflare_connections (org_id TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE, name TEXT NOT NULL, account_id TEXT NOT NULL, tunnel_id TEXT NOT NULL, tunnel_token_enc BLOB, api_token_enc BLOB, access_app_id TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL DEFAULT '', connector TEXT NOT NULL, metrics_addr TEXT NOT NULL DEFAULT '', prune INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
INSERT INTO orgs VALUES ('org_legacy', 'legacy', 123);
INSERT INTO services VALUES ('svc_legacy', 'org_legacy', 'legacy', 'exec', '{"exec":{"start":["start"]},"ready_tcp":"127.0.0.1:8080"}', 10, 20, 123, 456);
INSERT INTO cloudflare_connections (org_id, name, account_id, tunnel_id, tunnel_token_enc, connector, updated_at) VALUES ('org_legacy', 'legacy', 'account', 'tunnel', x'00ff01', 'external', 456);
`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		data, err := Open(path)
		if data != nil {
			data.Close()
		}
		if err == nil {
			t.Fatal("legacy ciphertext migrated without matching key")
		}
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var ciphertext []byte
		if err := raw.QueryRow(`SELECT tunnel_token_enc FROM cloudflare_connections WHERE org_id='org_legacy'`).Scan(&ciphertext); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ciphertext, []byte{0, 255, 1}) {
			t.Fatal("ciphertext modified after rejected open")
		}
		raw.Close()
	}
}

func TestShouldSerializeAliasWritesAcrossSQLiteStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	firstStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	orgID := storetest.CreateTypedOrg(t, firstStore)
	connection := storetest.CreateTypedConnection(t, firstStore, orgID)
	endpoint := storetest.CreateTypedEndpoint(t, firstStore, orgID, "")
	other := storetest.CreateTypedEndpoint(t, firstStore, orgID, "")
	other.Domain = "shared.example"
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- firstStore.ReplaceConnectionAliases(context.Background(), orgID, connection.ID, []store.EndpointAlias{{Hostname: "shared.example", EndpointID: endpoint.ID}})
	}()
	go func() { <-start; results <- secondStore.UpdateEndpoint(context.Background(), other) }()
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}
