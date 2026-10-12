package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestServiceCRUD(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"org_1", "org_2"} {
		if err := s.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	svc := &store.Service{ID: "svc_1", OrgID: "org_1", Name: "portainer", Driver: "docker", Spec: []byte(`{"docker":{"containers":["portainer"]}}`),
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
	if got.Name != "portainer" || string(got.Spec) != string(svc.Spec) || got.IdleTimeoutSeconds != 1800 || got.StartTimeoutSeconds != 60 {
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

func TestCloudflareConnectionUpsert(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "acme", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCloudflareConnection(ctx, "org_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty: want ErrNotFound, got %v", err)
	}
	c := &store.CloudflareConnection{OrgID: "org_1", Name: "lab", AccountID: "acc", TunnelID: "tun", TunnelTokenEnc: []byte{1, 2, 3},
		Connector: store.CloudflareConnectorExternal, MetricsAddr: "127.0.0.1:20241", UpdatedAt: now}
	if err := s.SetCloudflareConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCloudflareConnection(ctx, "org_1")
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
	if len(list) != 1 || !list[0].Prune || string(list[0].APITokenEnc) != "\x09" || list[0].Connector != store.CloudflareConnectorManaged {
		t.Fatalf("upsert mismatch: %+v", list)
	}
	if err := s.DeleteCloudflareConnection(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCloudflareConnection(ctx, "org_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
}
