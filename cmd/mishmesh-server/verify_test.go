package main

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/store"
)

type queryCountingStore struct {
	store.DataStore
	listOrgs, listConnections, listAllConnections int
}

func (s *queryCountingStore) ListOrgs(ctx context.Context) ([]*store.Org, error) {
	s.listOrgs++
	return s.DataStore.ListOrgs(ctx)
}

func (s *queryCountingStore) ListConnections(ctx context.Context, orgID string) ([]*store.Connection, error) {
	s.listConnections++
	return s.DataStore.ListConnections(ctx, orgID)
}

func (s *queryCountingStore) ListAllConnections(ctx context.Context) ([]*store.Connection, error) {
	s.listAllConnections++
	return s.DataStore.ListAllConnections(ctx)
}

func TestShouldVerifyAllConnectionsWithSingleQuery(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Server{DataDSN: filepath.Join(directory, "many.db"), SecretKeyFile: filepath.Join(directory, "many.key")}
	data, box, err := openDataStoreWithBox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	now := time.Now()
	const orgCount = 12
	for i := 0; i < orgCount; i++ {
		id := "org" + strconv.Itoa(i)
		connID := "conn" + strconv.Itoa(i)
		if err := data.CreateOrg(context.Background(), &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		sealed, err := box.Seal([]byte(`{"tunnel_token":"synthetic"}`), "connection:"+id+":"+connID)
		if err != nil {
			t.Fatal(err)
		}
		if err := data.CreateConnection(context.Background(), &store.Connection{ID: connID, OrgID: id, Name: "conn", Type: "cloudflare", Config: []byte(`{}`), SecretsEnc: sealed, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	counting := &queryCountingStore{DataStore: data}
	if err := verifyConnections(counting, box, false); err != nil {
		t.Fatal(err)
	}
	if counting.listOrgs != 0 || counting.listConnections != 0 || counting.listAllConnections != 1 {
		t.Fatalf("queries: ListOrgs=%d ListConnections=%d ListAllConnections=%d, want 0/0/1", counting.listOrgs, counting.listConnections, counting.listAllConnections)
	}
}
