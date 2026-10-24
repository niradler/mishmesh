package connections

import (
	"context"
	"errors"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

type hostnameInstance struct {
	fakeInstance
	changes []string
}

func (i *hostnameInstance) AddHostname(_ context.Context, host, service string) error {
	i.changes = append(i.changes, "add "+host+" "+service)
	return nil
}

func (i *hostnameInstance) RemoveHostname(_ context.Context, host string) error {
	i.changes = append(i.changes, "remove "+host)
	return nil
}

func TestShouldRouteHostnameChangesOnlyToCurrentManagingInstance(t *testing.T) {
	data := connectionStore(t, "one")
	instance := &hostnameInstance{fakeInstance: fakeInstance{status: Status{Health: "up"}}}
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return instance, nil }), nil)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.AddHostnameIfCurrent(context.Background(), *row, "vault.example.com", "http://localhost:8000"); err != nil {
		t.Fatal(err)
	}
	if err := hub.RemoveHostnameIfCurrent(context.Background(), *row, "vault.example.com"); err != nil {
		t.Fatal(err)
	}
	if len(instance.changes) != 2 || instance.changes[0] != "add vault.example.com http://localhost:8000" || instance.changes[1] != "remove vault.example.com" {
		t.Fatalf("changes = %v", instance.changes)
	}
	stale := *row
	stale.Name = "edited elsewhere"
	if err := hub.RemoveHostnameIfCurrent(context.Background(), stale, "vault.example.com"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale = %v", err)
	}
	missing := *row
	missing.ID = "missing"
	if err := hub.AddHostnameIfCurrent(context.Background(), missing, "vault.example.com", "http://localhost:8000"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("missing = %v", err)
	}
	if len(instance.changes) != 2 {
		t.Fatalf("rejected change reached instance: %v", instance.changes)
	}
}

func TestShouldReportUnsupportedHostnameManagement(t *testing.T) {
	data := connectionStore(t, "one")
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return &fakeInstance{status: Status{Health: "up"}}, nil }), nil)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	var hostnameErr *HostnameError
	if err := hub.RemoveHostnameIfCurrent(context.Background(), *row, "vault.example.com"); !errors.As(err, &hostnameErr) || hostnameErr.Kind != HostnameUnsupported {
		t.Fatalf("err = %v", err)
	}
}
