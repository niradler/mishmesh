package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldHideConfigAndRuntimeDetailsWhenProviderIsUnavailable(t *testing.T) {
	for _, unavailableRegistry := range []*connections.Registry{nil, connections.NewRegistry()} {
		f := newNetworkFixture(t)
		row := &store.Connection{ID: "removed-provider", OrgID: defaultOrgID, Name: "nested-credential-sentinel", Type: "third", Config: json.RawMessage(`{"sensitive":"opaque-credential-sentinel","nested":{"arbitrary":"nested-credential-sentinel"}}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := f.api.data.CreateConnection(context.Background(), row); err != nil {
			t.Fatal(err)
		}
		f.hub.mu.Lock()
		f.hub.statuses[row.ID] = connections.Status{Health: "up", LastError: "opaque-credential-sentinel", Details: map[string]any{"arbitrary": "nested-credential-sentinel"}, Hostnames: []connections.HostStatus{{Host: "opaque-credential-sentinel.example", State: "published"}}}
		f.hub.mu.Unlock()
		f.api.SetConnections(f.hub, unavailableRegistry, f.api.secretBox)
		for _, path := range []string{"/api/v1/connections/" + row.ID, "/api/v1/connections", "/api/v1/topology"} {
			code, body := statusAndBody(t, f.srv, http.MethodGet, path, "")
			if code != http.StatusOK || strings.Contains(body, "credential-sentinel") {
				t.Fatalf("unavailable provider %s: %d %s", path, code, body)
			}
			if path == "/api/v1/connections/"+row.ID {
				var view struct {
					Config map[string]any     `json:"config"`
					Status connections.Status `json:"status"`
				}
				if err := json.Unmarshal([]byte(body), &view); err != nil || len(view.Config) != 0 || view.Status.Health != "down" || view.Status.LastError != string(connections.HostUnknownType) {
					t.Fatalf("unsafe unavailable provider view: %s", body)
				}
			}
		}
		saved, err := f.api.data.GetConnection(context.Background(), row.ID)
		if err != nil || string(saved.Config) != string(row.Config) {
			t.Fatalf("view changed retained config: %+v %v", saved, err)
		}
	}
}

func TestShouldMaskServiceDiagnosticsInTopology(t *testing.T) {
	f := newNetworkFixture(t)
	created := f.createService(t, dockerServiceBody)
	f.svc.mu.Lock()
	f.svc.statuses[created.ID] = services.Status{State: services.StateError, LastError: "driver failed with opaque-credential-sentinel"}
	f.svc.mu.Unlock()
	for _, path := range []string{"/api/v1/services/" + created.ID, "/api/v1/services", "/api/v1/topology"} {
		code, body := statusAndBody(t, f.srv, http.MethodGet, path, "")
		if code != http.StatusOK || strings.Contains(body, "opaque-credential-sentinel") || !strings.Contains(body, "service operation failed") {
			t.Fatalf("service diagnostic %s: %d %s", path, code, body)
		}
	}
}
