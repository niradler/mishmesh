package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
)

type runtimeConnectionProvider struct {
	thirdConnectionProvider
	open func(connections.Config) (connections.Instance, error)
}

func (provider runtimeConnectionProvider) Open(cfg connections.Config, _ connections.Deps) (connections.Instance, error) {
	return provider.open(cfg)
}

type runtimeConnectionInstance struct{ stop func() }

func (*runtimeConnectionInstance) Run(ctx context.Context) { <-ctx.Done() }
func (*runtimeConnectionInstance) Status() connections.Status {
	return connections.Status{Health: "up"}
}
func (instance *runtimeConnectionInstance) Sync(context.Context) (connections.Status, error) {
	return instance.Status(), nil
}
func (instance *runtimeConnectionInstance) Close() {
	if instance.stop != nil {
		instance.stop()
	}
}

type cancelCommittedConnectionData struct {
	store.DataStore
	cancel context.CancelFunc
}

func (data cancelCommittedConnectionData) UpdateConnectionIfCurrent(ctx context.Context, record, expected *store.Connection) error {
	err := data.DataStore.UpdateConnectionIfCurrent(ctx, record, expected)
	if err == nil {
		data.cancel()
	}
	return err
}

func TestShouldExposeCommittedCanceledReloadFailureOnLaterGET(t *testing.T) {
	api, server := newNetworkTestAPI(t)
	registry := connections.NewRegistry()
	if err := registry.Register(runtimeConnectionProvider{open: func(connections.Config) (connections.Instance, error) { return &runtimeConnectionInstance{}, nil }}); err != nil {
		t.Fatal(err)
	}
	box := testSecretBox(t)
	hub := connections.NewHub(connections.HubOptions{Registry: registry, Deps: connections.Deps{Data: api.data, Box: box}})
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	api.SetConnections(hub, registry, box)
	send := func(method, path, body string) (int, string) { return statusAndBody(t, server, method, path, body) }
	id := createTestConnection(t, send, connectionCases()[2], "initial")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api.data = cancelCommittedConnectionData{DataStore: api.data, cancel: cancel}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/connections/"+id, strings.NewReader(`{"name":"committed-new","config":{"location":"new"}}`)).WithContext(ctx)
	request.SetPathValue("id", id)
	request = request.WithContext(api.authContext(request, defaultOrgID, "owner", store.RoleOwner, true))
	recorder := httptest.NewRecorder()
	api.updateConnectionHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("committed response: %d %s", recorder.Code, recorder.Body.String())
	}
	code, response := send(http.MethodGet, "/api/v1/connections/"+id, "")
	var view struct {
		Name   string             `json:"name"`
		Status connections.Status `json:"status"`
	}
	if err := json.Unmarshal([]byte(response), &view); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || view.Name != "committed-new" || view.Status.Health == "up" || !strings.Contains(view.Status.LastError, "canceled") || strings.Contains(response, "credential-value") {
		t.Fatalf("later GET lost committed reload failure: %d %s", code, response)
	}
	code, response = send(http.MethodGet, "/api/v1/connections", "")
	if code != http.StatusOK || !strings.Contains(response, "canceled before admission") || strings.Contains(response, "credential-value") {
		t.Fatalf("list lost saved failure: %d %s", code, response)
	}
}

func TestShouldKeepTrustedDrainPhaseDiagnosticOnLaterGET(t *testing.T) {
	api, server := newNetworkTestAPI(t)
	registry := connections.NewRegistry()
	release := make(chan struct{})
	defer close(release)
	var opens atomic.Int32
	if err := registry.Register(runtimeConnectionProvider{open: func(connections.Config) (connections.Instance, error) {
		instance := &runtimeConnectionInstance{}
		if opens.Add(1) == 1 {
			instance.stop = func() { <-release }
		}
		return instance, nil
	}}); err != nil {
		t.Fatal(err)
	}
	box := testSecretBox(t)
	hub := connections.NewHub(connections.HubOptions{Registry: registry, Deps: connections.Deps{Data: api.data, Box: box}, DrainTimeout: 10 * time.Millisecond})
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	api.SetConnections(hub, registry, box)
	send := func(method, path, body string) (int, string) { return statusAndBody(t, server, method, path, body) }
	id := createTestConnection(t, send, connectionCases()[2], "initial")
	code, response := send(http.MethodPut, "/api/v1/connections/"+id, `{"name":"saved-new","config":{}}`)
	if code != http.StatusOK || !strings.Contains(response, "drain the previous generation") || !strings.Contains(response, "Connection saved") {
		t.Fatalf("committed drain failure: %d %s", code, response)
	}
	code, response = send(http.MethodGet, "/api/v1/connections/"+id, "")
	if code != http.StatusOK || !strings.Contains(response, "drain the previous generation") || strings.Contains(response, "credential-value") || opens.Load() != 1 {
		t.Fatalf("GET erased drain phase: %d %s opens=%d", code, response, opens.Load())
	}
}

func TestShouldKeepTrustedLoadPhaseDiagnosticOnGETWithoutProviderSecretEcho(t *testing.T) {
	api, server := newNetworkTestAPI(t)
	registry := connections.NewRegistry()
	if err := registry.Register(runtimeConnectionProvider{open: func(cfg connections.Config) (connections.Instance, error) {
		return nil, fmt.Errorf("raw provider failure: %s", cfg.Secrets["third_key"])
	}}); err != nil {
		t.Fatal(err)
	}
	box := testSecretBox(t)
	hub := connections.NewHub(connections.HubOptions{Registry: registry, Deps: connections.Deps{Data: api.data, Box: box}})
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	api.SetConnections(hub, registry, box)
	send := func(method, path, body string) (int, string) { return statusAndBody(t, server, method, path, body) }
	id := createTestConnection(t, send, connectionCases()[2], "failed-open")
	code, response := send(http.MethodGet, "/api/v1/connections/"+id, "")
	if code != http.StatusOK || !strings.Contains(response, "open connection failed") || strings.Contains(response, "credential-value") || strings.Contains(response, "raw provider failure") {
		t.Fatalf("trusted phase diagnostic lost: %d %s", code, response)
	}
}
