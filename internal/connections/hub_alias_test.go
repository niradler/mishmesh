package connections

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestShouldFenceDurableHubAliasPublicationWithEncryptedGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "aliases.db")
	data, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = data.Close() }()
	now := time.Now()
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := box.Seal([]byte(`{"token":"private-sentinel"}`), SecretContext("org", "network"))
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		data.CreateOrg(ctx, &store.Org{ID: "org", Name: "org", CreatedAt: now}),
		data.CreateAgent(ctx, &store.Agent{ID: "agent", OrgID: "org", Name: "agent", Status: store.AgentActive, CreatedAt: now}),
		data.CreateConnection(ctx, &store.Connection{ID: "network", OrgID: "org", Name: "network", Type: "fake", Config: json.RawMessage(`{}`), SecretsEnc: cipher, CreatedAt: now, UpdatedAt: now}),
		data.CreateEndpoint(ctx, &store.Endpoint{ID: "endpoint", AgentID: "agent", OrgID: "org", Kind: store.KindHTTP, Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "network", Hostname: "notes"}}}, CreatedAt: now}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	provider := testProvider(func(cfg Config, deps Deps) (Instance, error) {
		if len(cfg.Record.SecretsEnc) != 0 || cfg.Secrets["token"] != "private-sentinel" {
			t.Fatal("provider secret contract changed")
		}
		return &fakeInstance{sync: func(ctx context.Context) (Status, error) {
			status := Status{Health: "healthy", Hostnames: []HostStatus{{Host: "notes.tailnet.ts.net", EndpointID: "endpoint", State: "published", EndpointKind: store.KindHTTP, AccessHostname: "notes", AccessHostnameKnown: true}}}
			err := RecordAccessAliases(ctx, cfg.Record, Descriptor{Role: "access", Protocols: []string{store.KindHTTP}}, &status, deps)
			return status, err
		}}, nil
	})
	provider.descriptor.Protocols = []string{store.KindHTTP}
	hub := testHub(t, data, provider, box)
	status, err := hub.Sync(ctx, "network")
	if err != nil || status.Hostnames[0].State != "published" {
		t.Fatalf("publication %+v %v", status, err)
	}
	if ep, err := data.GetEndpointByAccessHostname(ctx, "NOTES.TAILNET.TS.NET."); err != nil || ep.ID != "endpoint" {
		t.Fatalf("durable routing %v %v", ep, err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if ep, err := data.GetEndpointByAccessHostname(ctx, "notes.tailnet.ts.net"); err != nil || ep.ID != "endpoint" {
		t.Fatalf("reopened routing %v %v", ep, err)
	}
	hub.Close()
	provider.open = func(cfg Config, deps Deps) (Instance, error) {
		return &fakeInstance{sync: func(ctx context.Context) (Status, error) {
			status := Status{Hostnames: []HostStatus{{Host: "notes.old.ts.net", EndpointID: "endpoint", State: "published", EndpointKind: store.KindHTTP, AccessHostname: "notes", AccessHostnameKnown: true}}}
			err := RecordAccessAliases(ctx, cfg.Record, provider.descriptor, &status, deps)
			return status, err
		}}, nil
	}
	hub = testHub(t, data, provider, box)
	record, err := data.GetConnection(ctx, "network")
	if err != nil {
		t.Fatal(err)
	}
	record.Config = json.RawMessage(`{"tailnet":"new"}`)
	if err := data.UpdateConnection(ctx, record); err != nil {
		t.Fatal(err)
	}
	status, err = hub.Sync(ctx, "network")
	if err == nil || len(status.Hostnames) > 0 && status.Hostnames[0].State == "published" {
		t.Fatalf("stale generation published %+v %v", status, err)
	}
	if _, err := data.GetEndpointByAccessHostname(ctx, "notes.old.ts.net"); err != store.ErrNotFound {
		t.Fatalf("stale route exists %v", err)
	}
}
