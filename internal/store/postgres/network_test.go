package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestServiceCRUD(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"org_pgn1", "org_pgn2"} {
		if err := s.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	svc := &store.Service{ID: "svc_pgn1", OrgID: "org_pgn1", Name: "portainer", Driver: "docker", Spec: []byte(`{"docker":{"containers":["portainer"]}}`),
		IdleTimeoutSeconds: 1800, StartTimeoutSeconds: 60, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	dup := *svc
	dup.ID = "svc_pgn2"
	if err := s.CreateService(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: want ErrConflict, got %v", err)
	}
	other := *svc
	other.ID, other.OrgID = "svc_pgn3", "org_pgn2"
	if err := s.CreateService(ctx, &other); err != nil {
		t.Fatalf("same name in other org: %v", err)
	}

	got, err := s.GetService(ctx, "svc_pgn1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "portainer" || string(got.Spec) != string(svc.Spec) || got.IdleTimeoutSeconds != 1800 || got.StartTimeoutSeconds != 60 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	got.Name = "portainer-ce"
	got.IdleTimeoutSeconds = 0
	if err := s.UpdateService(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListServices(ctx, "org_pgn1")
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
	mine := 0
	for _, a := range all {
		if a.OrgID == "org_pgn1" || a.OrgID == "org_pgn2" {
			mine++
		}
	}
	if mine != 2 {
		t.Fatalf("all services: got %d want 2", mine)
	}

	if err := s.DeleteService(ctx, "svc_pgn1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetService(ctx, "svc_pgn1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	if err := s.DeleteService(ctx, "svc_pgn1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
	if err := s.UpdateService(ctx, got); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: want ErrNotFound, got %v", err)
	}
}

func TestCloudflareConnectionUpsert(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.CreateOrg(ctx, &store.Org{ID: "org_pgc1", Name: "acme", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCloudflareConnection(ctx, "org_pgc1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty: want ErrNotFound, got %v", err)
	}
	c := &store.CloudflareConnection{OrgID: "org_pgc1", Name: "lab", AccountID: "acc", TunnelID: "tun", TunnelTokenEnc: []byte{1, 2, 3},
		Connector: store.CloudflareConnectorExternal, MetricsAddr: "127.0.0.1:20241", UpdatedAt: now}
	if err := s.SetCloudflareConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCloudflareConnection(ctx, "org_pgc1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lab" || string(got.TunnelTokenEnc) != "\x01\x02\x03" || got.APITokenEnc != nil || got.Prune {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	c.APITokenEnc = []byte{9}
	c.Prune = true
	c.Connector = store.CloudflareConnectorManaged
	if err := s.SetCloudflareConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListCloudflareConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list = onlyOrg(list, "org_pgc1")
	if len(list) != 1 || !list[0].Prune || string(list[0].APITokenEnc) != "\x09" || list[0].Connector != store.CloudflareConnectorManaged {
		t.Fatalf("upsert mismatch: %+v", list)
	}
	if err := s.DeleteCloudflareConnection(ctx, "org_pgc1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCloudflareConnection(ctx, "org_pgc1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
}

func onlyOrg(list []*store.CloudflareConnection, orgID string) []*store.CloudflareConnection {
	var out []*store.CloudflareConnection
	for _, c := range list {
		if c.OrgID == orgID {
			out = append(out, c)
		}
	}
	return out
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
	connection := &store.Connection{ID: id, OrgID: orgID, Name: id, Type: "kubernetes", Config: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
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
	if string(got.Config) != string(dependent.Config) || string(got.Ready) != string(dependent.Ready) || len(got.Spec) != 0 || !reflect.DeepEqual(got.Requires, []string{first.ID}) || !reflect.DeepEqual(got.ConnectionIDs, []string{connection.ID}) {
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
	foreign := createTypedEndpoint(t, s, foreignID, store.NewID("canonical")+".example")
	host := strings.ReplaceAll(store.NewID("alias"), "_", "-") + ".example"
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
