package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldEnforceRequirementDAG(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC()
	orgID, otherOrgID := store.NewID("org"), store.NewID("org")
	for _, id := range []string{orgID, otherOrgID} {
		if err := s.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	makeService := func(name, org string) *store.Service {
		svc := &store.Service{ID: store.NewID("svc"), OrgID: org, Name: name, Driver: "custom", Config: json.RawMessage("{}"), CreatedAt: now, UpdatedAt: now}
		if err := s.CreateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
		return svc
	}
	a, b, c, d := makeService("alpha", orgID), makeService("beta", orgID), makeService("charlie", orgID), makeService("delta", orgID)
	private := makeService("private-name", otherOrgID)
	for _, svc := range []*store.Service{b, c, a} {
		svc.Requires = []string{d.ID}
		if svc == a {
			svc.Requires = []string{c.ID, b.ID, b.ID}
		}
		if err := s.UpdateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name     string
		requires []string
	}{
		{"long cycle", []string{a.ID}},
		{"self cycle", []string{d.ID}},
		{"missing", []string{"missing-private-id"}},
		{"cross org", []string{private.ID}},
		{"empty", []string{""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := *d
			changed.Name, changed.Config, changed.Requires = "changed", json.RawMessage("{\"changed\":true}"), test.requires
			err := s.UpdateService(ctx, &changed)
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("want conflict, got %v", err)
			}
			for _, secret := range []string{private.ID, private.Name, "missing-private-id"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
			got, err := s.GetService(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != d.Name || string(got.Config) != string(d.Config) || len(got.Requires) != 0 {
				t.Fatalf("failed update changed row: %+v", got)
			}
		})
	}
	t.Run("failed create", func(t *testing.T) {
		invalid := *d
		invalid.ID, invalid.Name, invalid.Requires = store.NewID("svc"), "invalid", []string{private.ID}
		if err := s.CreateService(ctx, &invalid); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("want conflict, got %v", err)
		}
		if _, err := s.GetService(ctx, invalid.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("create persisted: %v", err)
		}
	})
	t.Run("foreign owner update", func(t *testing.T) {
		changed := *a
		changed.OrgID = otherOrgID
		if err := s.UpdateService(ctx, &changed); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign owner update: %v", err)
		}
	})
	t.Run("late reference failure restores row and edges", func(t *testing.T) {
		changed := *a
		changed.Name, changed.Requires, changed.ConnectionIDs = "changed", []string{d.ID}, []string{"missing-connection"}
		if err := s.UpdateService(ctx, &changed); err == nil {
			t.Fatal("invalid connection accepted")
		}
		got, err := s.GetService(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{b.ID, c.ID}
		if want[0] > want[1] {
			want[0], want[1] = want[1], want[0]
		}
		if got.Name != a.Name || !reflect.DeepEqual(got.Requires, want) {
			t.Fatalf("failed update changed row/edges: %+v", got)
		}
	})
	dependents, err := s.ListServiceDependents(ctx, orgID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependents) != 2 || dependents[0].Name != "beta" || dependents[1].Name != "charlie" {
		t.Fatalf("dependents: %+v", dependents)
	}
	otherDependents, err := s.ListServiceDependents(ctx, otherOrgID, d.ID)
	if err != nil || len(otherDependents) != 0 {
		t.Fatalf("cross-org dependents: %+v, %v", otherDependents, err)
	}
	if err := s.DeleteService(ctx, d.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("delete required: %v", err)
	}
	dependents, err = s.ListServiceDependents(ctx, orgID, d.ID)
	if err != nil || len(dependents) != 2 {
		t.Fatalf("delete changed inbound edges: %+v, %v", dependents, err)
	}
	a.Requires = []string{d.ID}
	if err := s.UpdateService(ctx, a); err != nil {
		t.Fatal(err)
	}
	b.Requires = []string{a.ID}
	if err := s.UpdateService(ctx, b); err != nil {
		t.Fatalf("replacement retained old edge: %v", err)
	}
}

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

func TestShouldKeepMultipleConnectionsOfOneType(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	orgID := store.NewID("org")
	otherOrgID := store.NewID("org")
	for _, id := range []string{orgID, otherOrgID} {
		if err := s.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	c := &store.Connection{ID: store.NewID("conn"), OrgID: orgID, Type: "cloudflare", Name: "lab", Config: json.RawMessage(`{"level":"managed"}`), SecretsEnc: []byte{0, 255, 1}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	backup := *c
	backup.ID, backup.Name = store.NewID("conn"), "backup"
	if err := s.CreateConnection(ctx, &backup); err != nil {
		t.Fatal(err)
	}
	dup := *c
	dup.ID = store.NewID("conn")
	if err := s.CreateConnection(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	dup.OrgID = otherOrgID
	if err := s.CreateConnection(ctx, &dup); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != c.Type || !bytes.Equal(got.Config, c.Config) || !bytes.Equal(got.SecretsEnc, c.SecretsEnc) || !got.CreatedAt.Equal(now) {
		t.Fatalf("connection round trip: %+v", got)
	}
	got.Name, got.Config, got.SecretsEnc = "updated", json.RawMessage(`{}`), nil
	if err := s.UpdateConnection(ctx, got); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListConnections(ctx, orgID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	all, err := s.ListAllConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range all {
		if row.OrgID == orgID || row.OrgID == otherOrgID {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("all connections: %d", count)
	}
	got.OrgID = otherOrgID
	if err := s.UpdateConnection(ctx, got); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	if err := s.DeleteConnection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing delete: %v", err)
	}
}

func createTypedOrg(t *testing.T, s *Store) string {
	t.Helper()
	id := store.NewID("org")
	if err := s.CreateOrg(context.Background(), &store.Org{ID: id, Name: id, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return id
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
	orgID := createTypedOrg(t, firstStore)
	connection := createTypedConnection(t, firstStore, orgID)
	endpoint := createTypedEndpoint(t, firstStore, orgID, "")
	other := createTypedEndpoint(t, firstStore, orgID, "")
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

func createTypedService(t *testing.T, s *Store, orgID string) *store.Service {
	t.Helper()
	id := store.NewID("svc")
	now := time.Now().UTC()
	svc := &store.Service{ID: id, OrgID: orgID, Name: id, Driver: "docker", Config: json.RawMessage(`{"containers":["one"]}`), Ready: json.RawMessage(`{"tcp":"127.0.0.1:8080"}`), CreatedAt: now, UpdatedAt: now}
	if err := s.CreateService(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	return svc
}

func createTypedConnection(t *testing.T, s *Store, orgID string) *store.Connection {
	t.Helper()
	id := store.NewID("conn")
	now := time.Now().UTC()
	connection := &store.Connection{ID: id, OrgID: orgID, Name: id, Type: "example-runtime", Config: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
	if err := s.CreateConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	return connection
}

func createTypedEndpoint(t *testing.T, s *Store, orgID, domain string) *store.Endpoint {
	t.Helper()
	agentID := store.NewID("ag")
	now := time.Now().UTC()
	if err := s.CreateAgent(context.Background(), &store.Agent{ID: agentID, OrgID: orgID, Name: agentID, Status: store.AgentActive, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	endpoint := &store.Endpoint{ID: store.NewID("ep"), OrgID: orgID, AgentID: agentID, Kind: store.KindHTTP, Lifecycle: store.LifecycleReserved, Domain: domain, CreatedAt: now}
	if err := s.CreateEndpoint(context.Background(), endpoint); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestShouldReplaceTypedServiceReferencesAtomically(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orgID, foreignID := createTypedOrg(t, s), createTypedOrg(t, s)
	first, second, dependent := createTypedService(t, s, orgID), createTypedService(t, s, orgID), createTypedService(t, s, orgID)
	foreign := createTypedService(t, s, foreignID)
	connection := createTypedConnection(t, s, orgID)
	otherConnection := createTypedConnection(t, s, orgID)
	foreignConnection := createTypedConnection(t, s, foreignID)
	dependent.Requires = []string{first.ID, first.ID}
	dependent.ConnectionIDs = []string{connection.ID, connection.ID}
	if err := s.UpdateService(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetService(ctx, dependent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Config) != string(dependent.Config) || string(got.Ready) != string(dependent.Ready) || !reflect.DeepEqual(got.Requires, []string{first.ID}) || !reflect.DeepEqual(got.ConnectionIDs, []string{connection.ID}) {
		t.Fatalf("typed round trip: %+v", got)
	}
	original := *got
	for _, test := range []struct {
		name                  string
		requires, connections []string
	}{
		{"missing requirement", []string{"svc_missing"}, []string{otherConnection.ID}},
		{"foreign requirement", []string{foreign.ID}, []string{otherConnection.ID}},
		{"self requirement", []string{dependent.ID}, []string{otherConnection.ID}},
		{"missing connection", []string{second.ID}, []string{"conn_missing"}},
		{"foreign connection", []string{second.ID}, []string{foreignConnection.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := original
			changed.Name = store.NewID("changed")
			changed.Config, changed.Ready = json.RawMessage(`{}`), nil
			changed.Requires, changed.ConnectionIDs = test.requires, test.connections
			if err := s.UpdateService(ctx, &changed); err == nil {
				t.Fatal("invalid reference accepted")
			}
			restored, err := s.GetService(ctx, dependent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored, &original) {
				t.Fatalf("failed replacement changed service: %+v", restored)
			}
		})
	}
	foreignUpdate := original
	foreignUpdate.OrgID = foreignID
	if err := s.UpdateService(ctx, &foreignUpdate); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	createInvalid := original
	createInvalid.ID, createInvalid.Name = store.NewID("svc"), store.NewID("invalid")
	createInvalid.Requires = []string{"svc_missing"}
	if err := s.CreateService(ctx, &createInvalid); err == nil {
		t.Fatal("invalid create accepted")
	}
	if _, err := s.GetService(ctx, createInvalid.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed creation persisted: %v", err)
	}
	dependent.Requires, dependent.ConnectionIDs = []string{second.ID}, []string{otherConnection.ID}
	if err := s.UpdateService(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	for _, list := range []func(context.Context) ([]*store.Service, error){func(ctx context.Context) ([]*store.Service, error) { return s.ListServices(ctx, orgID) }, s.ListAllServices} {
		rows, err := list(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range rows {
			if row.ID == dependent.ID {
				found = true
				if !reflect.DeepEqual(row.Requires, []string{second.ID}) || !reflect.DeepEqual(row.ConnectionIDs, []string{otherConnection.ID}) {
					t.Fatalf("replacement list: %+v", row)
				}
			}
		}
		if !found {
			t.Fatal("dependent missing from list")
		}
	}
	if err := s.DeleteService(ctx, second.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("required delete: %v", err)
	}
	if err := s.DeleteConnection(ctx, otherConnection.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("referenced connection delete: %v", err)
	}
	if err := s.DeleteService(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	dependent.Requires, dependent.ConnectionIDs = nil, nil
	if err := s.UpdateService(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteService(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, otherConnection.ID); err != nil {
		t.Fatal(err)
	}
}

func TestShouldCreateTypedServiceWithReferencesAndRejectInvalidDocuments(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orgID := createTypedOrg(t, s)
	dependency := createTypedService(t, s, orgID)
	connection := createTypedConnection(t, s, orgID)
	now := time.Now().UTC()
	svc := &store.Service{ID: store.NewID("svc"), OrgID: orgID, Name: store.NewID("typed"), Driver: "custom", Config: json.RawMessage(`{"arbitrary_runtime_field":"` + connection.ID + `"}`), Ready: json.RawMessage(`{"http":{"url":"http://127.0.0.1:8080/","status":200}}`), Requires: []string{dependency.ID, dependency.ID}, ConnectionIDs: []string{connection.ID, connection.ID}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetService(ctx, svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Config, svc.Config) || !bytes.Equal(got.Ready, svc.Ready) || !reflect.DeepEqual(got.Requires, []string{dependency.ID}) || !reflect.DeepEqual(got.ConnectionIDs, []string{connection.ID}) {
		t.Fatalf("typed create: %+v", got)
	}
	for _, test := range []struct {
		name          string
		config, ready json.RawMessage
	}{
		{"invalid config", json.RawMessage(`{"broken":`), svc.Ready},
		{"invalid readiness", svc.Config, json.RawMessage(`{"broken":`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *got
			changed.Config, changed.Ready = test.config, test.ready
			if err := s.UpdateService(ctx, &changed); err == nil {
				t.Fatal("invalid JSON accepted")
			}
			restored, err := s.GetService(ctx, svc.ID)
			if err != nil || !reflect.DeepEqual(restored, got) {
				t.Fatalf("invalid document changed stored row: %+v %v", restored, err)
			}
		})
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("ConnectionIDs")) {
		t.Fatal("derived connection IDs exposed in JSON")
	}
	connection.SecretsEnc = []byte("sealed-private-data")
	encoded, err = json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("SecretsEnc")) {
		t.Fatal("sealed secrets exposed in JSON")
	}
	if err := s.DeleteService(ctx, svc.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteService(ctx, dependency.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
}

func TestShouldValidateEndpointReferencesAndGuardDeletes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orgID, foreignID := createTypedOrg(t, s), createTypedOrg(t, s)
	endpoint := createTypedEndpoint(t, s, orgID, "")
	service := createTypedService(t, s, orgID)
	foreignService := createTypedService(t, s, foreignID)
	connection := createTypedConnection(t, s, orgID)
	foreignConnection := createTypedConnection(t, s, foreignID)
	endpoint.Policy = &store.EndpointPolicy{ServiceID: service.ID, Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: "tail.example"}}}
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteService(ctx, service.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("endpoint-linked delete: %v", err)
	}
	if err := s.DeleteConnection(ctx, connection.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("access-linked delete: %v", err)
	}
	for _, policy := range []*store.EndpointPolicy{{ServiceID: foreignService.ID}, {ServiceID: "svc_missing"}, {Access: []store.AccessRef{{ConnectionID: foreignConnection.ID}}}, {Access: []store.AccessRef{{ConnectionID: "conn_missing"}}}} {
		changed := *endpoint
		changed.Policy = policy
		if err := s.UpdateEndpoint(ctx, &changed); err == nil {
			t.Fatal("foreign/missing endpoint reference accepted")
		}
		changed.ID = store.NewID("ep")
		if err := s.CreateEndpoint(ctx, &changed); err == nil {
			t.Fatal("foreign/missing endpoint reference create accepted")
		}
	}
	endpoint.Policy = nil
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteService(ctx, service.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
}

func TestShouldValidateAliasOwnersAndHostnameCollisions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orgID, foreignID := createTypedOrg(t, s), createTypedOrg(t, s)
	connection, other := createTypedConnection(t, s, orgID), createTypedConnection(t, s, foreignID)
	endpoint := createTypedEndpoint(t, s, orgID, "")
	foreign := createTypedEndpoint(t, s, foreignID, strings.ReplaceAll(store.NewID("canonical"), "_", "-")+".example")
	host := strings.ReplaceAll(store.NewID("alias"), "_", "-") + ".example"
	endpoint.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: host}}}
	foreign.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: other.ID, Hostname: host}}}
	for _, row := range []*store.Endpoint{endpoint, foreign} {
		if err := s.UpdateEndpoint(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	original := []store.EndpointAlias{{Hostname: strings.ToUpper(host) + ".", EndpointID: endpoint.ID, OrgID: foreignID, ConnectionID: other.ID}}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, original); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEndpointByAccessHostname(ctx, host)
	if err != nil || got.ID != endpoint.ID || got.OrgID != orgID {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if err := s.ReplaceConnectionAliases(ctx, foreignID, other.ID, []store.EndpointAlias{{Hostname: host, EndpointID: foreign.ID}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("cross-org collision: %v", err)
	}
	for _, test := range []struct {
		name, orgID, connectionID string
		aliases                   []store.EndpointAlias
	}{
		{"foreign endpoint with forged owner", orgID, connection.ID, []store.EndpointAlias{{Hostname: "forged.example", EndpointID: foreign.ID, OrgID: orgID, ConnectionID: connection.ID}}},
		{"foreign connection", orgID, other.ID, nil},
		{"missing endpoint", orgID, connection.ID, []store.EndpointAlias{{Hostname: "missing.example", EndpointID: "ep_missing"}}},
		{"canonical collision", orgID, connection.ID, []store.EndpointAlias{{Hostname: foreign.Domain, EndpointID: endpoint.ID}}},
		{"invalid hostname", orgID, connection.ID, []store.EndpointAlias{{Hostname: "https://bad.example", EndpointID: endpoint.ID}}},
		{"replacement collision rollback", orgID, connection.ID, []store.EndpointAlias{{Hostname: "new.example", EndpointID: endpoint.ID}, {Hostname: foreign.Domain, EndpointID: endpoint.ID}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for i := range test.aliases {
				test.aliases[i].AccessHostname = host
			}
			if err := s.ReplaceConnectionAliases(ctx, test.orgID, test.connectionID, test.aliases); err == nil {
				t.Fatal("invalid alias replacement accepted")
			}
			got, err := s.GetEndpointByAccessHostname(ctx, host)
			if err != nil || got.ID != endpoint.ID {
				t.Fatalf("replacement lost original: %+v %v", got, err)
			}
		})
	}
	foreign.Domain = host
	if err := s.UpdateEndpoint(ctx, foreign); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reverse canonical update: %v", err)
	}
	newEndpoint := *foreign
	newEndpoint.ID = store.NewID("ep")
	if err := s.CreateEndpoint(ctx, &newEndpoint); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reverse canonical create: %v", err)
	}
	if err := s.DeleteConnection(ctx, connection.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("alias connection delete: %v", err)
	}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, host); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cleared alias: %v", err)
	}
	if err := s.UpdateEndpoint(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	endpoint.Policy.Access = nil
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
}

func TestShouldSerializeConflictingAliasReplacements(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	orgID := createTypedOrg(t, s)
	first, second := createTypedConnection(t, s, orgID), createTypedConnection(t, s, orgID)
	endpoint, other := createTypedEndpoint(t, s, orgID, ""), createTypedEndpoint(t, s, orgID, "")
	start := make(chan struct{})
	results := make(chan error, 2)
	host := strings.ReplaceAll(store.NewID("race"), "_", "-") + ".example"
	endpoint.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: first.ID, Hostname: host}}}
	other.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: second.ID, Hostname: host}}}
	for _, row := range []*store.Endpoint{endpoint, other} {
		if err := s.UpdateEndpoint(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ connection, endpoint string }{{first.ID, endpoint.ID}, {second.ID, other.ID}} {
		go func(connectionID, endpointID string) {
			<-start
			results <- s.ReplaceConnectionAliases(ctx, orgID, connectionID, []store.EndpointAlias{{Hostname: host, EndpointID: endpointID}})
		}(test.connection, test.endpoint)
	}
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

func TestServiceCRUD(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"org_1", "org_2"} {
		if err := s.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	svc := &store.Service{ID: "svc_1", OrgID: "org_1", Name: "portainer", Driver: "docker", Config: []byte(`{"containers":["portainer"]}`),
		IdleTimeoutSeconds: 1800, StartTimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	dup := *svc
	dup.ID = "svc_2"
	if err := s.CreateService(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: want ErrConflict, got %v", err)
	}
	other := *svc
	other.ID, other.OrgID = "svc_3", "org_2"
	if err := s.CreateService(ctx, &other); err != nil {
		t.Fatalf("same name in other org: %v", err)
	}

	got, err := s.GetService(ctx, "svc_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "portainer" || string(got.Config) != string(svc.Config) || got.IdleTimeoutSeconds != 1800 || got.StartTimeoutSeconds != 60 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	got.Name = "portainer-ce"
	got.IdleTimeoutSeconds = 0
	if err := s.UpdateService(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListServices(ctx, "org_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "portainer-ce" || list[0].IdleTimeoutSeconds != 0 {
		t.Fatalf("org list: %+v", list)
	}
	all, err := s.ListAllServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all services: got %d want 2", len(all))
	}

	if err := s.DeleteService(ctx, "svc_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetService(ctx, "svc_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	if err := s.DeleteService(ctx, "svc_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
	if err := s.UpdateService(ctx, got); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: want ErrNotFound, got %v", err)
	}
}

func TestConnectionCRUDPreservesEncryptedConfiguration(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	org := store.NewID("org")
	now := time.Now().UTC()
	if err := s.CreateOrg(ctx, &store.Org{ID: org, Name: org, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	id := store.NewID("conn")
	if _, err := s.GetConnection(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty %v", err)
	}
	record := &store.Connection{ID: id, OrgID: org, Name: "lab", Type: "cloudflare", Config: json.RawMessage(`{"connector":"external","prune":false}`), SecretsEnc: []byte{1, 2, 3}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateConnection(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lab" || !bytes.Equal(got.Config, record.Config) || !bytes.Equal(got.SecretsEnc, record.SecretsEnc) {
		t.Fatalf("round trip %+v", got)
	}
	record.Config = json.RawMessage(`{"connector":"managed","prune":true}`)
	record.SecretsEnc = []byte{9}
	if err := s.UpdateConnection(ctx, record); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConnections(ctx, org)
	if err != nil || len(list) != 1 || !bytes.Equal(list[0].Config, record.Config) || !bytes.Equal(list[0].SecretsEnc, record.SecretsEnc) {
		t.Fatalf("update %+v %v", list, err)
	}
	if err := s.DeleteConnection(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnection(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete %v", err)
	}
}
