package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/connections"
	tailscaleconnection "github.com/mishmesh/mishmesh/internal/connections/tailscale"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
	"tailscale.com/ipn/ipnstate"
)

type fakeConnectionControl struct {
	mu          sync.Mutex
	statuses    map[string]connections.Status
	triggers    map[string]int
	reloads     map[string]int
	syncs       map[string]int
	reloadError error
	syncError   error
	hostnames   []string
	hostError   error
}

func newFakeConnectionControl() *fakeConnectionControl {
	return &fakeConnectionControl{statuses: map[string]connections.Status{}, triggers: map[string]int{}, reloads: map[string]int{}, syncs: map[string]int{}}
}

func (f *fakeConnectionControl) AddHostnameIfCurrent(_ context.Context, _ store.Connection, host, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostnames = append(f.hostnames, "add "+host+" "+service)
	return f.hostError
}

func (f *fakeConnectionControl) RemoveHostnameIfCurrent(_ context.Context, _ store.Connection, host string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostnames = append(f.hostnames, "remove "+host)
	return f.hostError
}

func (f *fakeConnectionControl) Status(id string) connections.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	if status, ok := f.statuses[id]; ok {
		return status
	}
	return connections.Status{Level: "imported", Health: "up", Details: map[string]any{"private_key": "hidden"}}
}

func (f *fakeConnectionControl) Reload(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads[id]++
	return f.reloadError
}

func (f *fakeConnectionControl) ReloadIfCurrent(ctx context.Context, expected store.Connection) error {
	return f.Reload(ctx, expected.ID)
}

func (f *fakeConnectionControl) Sync(_ context.Context, id string) (connections.Status, error) {
	f.mu.Lock()
	f.syncs[id]++
	err := f.syncError
	f.mu.Unlock()
	return f.Status(id), err
}

func (f *fakeConnectionControl) SyncIfCurrent(ctx context.Context, expected store.Connection) (connections.Status, error) {
	return f.Sync(ctx, expected.ID)
}

func (f *fakeConnectionControl) Trigger(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggers[id]++
}

func (f *fakeConnectionControl) triggerCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers[id]
}

type thirdConnectionProvider struct{ detect bool }

func (p thirdConnectionProvider) Describe() connections.Descriptor {
	return connections.Descriptor{Type: "third", Label: "Third provider", Role: "runtime", Detect: p.detect, Fields: []typedfields.Field{
		{Key: "third_key", Kind: "secret"}, {Key: "host_secret", Kind: "secret", HostCommand: true}, {Key: "location", Kind: "text"},
		{Key: "command", Kind: "list", HostCommand: true}, {Key: "sensitive", Kind: "text", Sensitive: true},
	}}
}

func (p thirdConnectionProvider) Validate(cfg connections.Config) error {
	if strings.Contains(string(cfg.Record.Config), "invalid") {
		return fmt.Errorf("provider rejects %s", cfg.Secrets["third_key"])
	}
	return nil
}

func (p thirdConnectionProvider) Detect(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	if input["location"] == "invalid" {
		return nil, fmt.Errorf("secret %v", input["third_key"])
	}
	return json.Marshal(map[string]any{"level": "managed", "third_key": input["third_key"], "summary": input["third_key"], "config": input, "nested": []any{map[string]any{"level": "managed"}}})
}

func (p thirdConnectionProvider) Open(connections.Config, connections.Deps) (connections.Instance, error) {
	return nil, errors.New("third fixture has no runtime")
}

type connectionTailLocal struct{ tailscaleconnection.LocalAPI }

func (connectionTailLocal) StatusWithoutPeers(context.Context) (*ipnstate.Status, error) {
	return &ipnstate.Status{Version: "fixture", CurrentTailnet: &ipnstate.TailnetStatus{Name: "example.com"}}, nil
}

func testConnectionRegistry(t *testing.T) *connections.Registry {
	t.Helper()
	registry := connections.NewRegistry()
	providers := []connections.Provider{
		cloudflare.NewProvider(cloudflare.ProviderOptions{Detect: func(context.Context, string, string) (cloudflare.DetectResult, error) {
			return cloudflare.DetectResult{AccountID: "acct1", TunnelID: "tun1", Hostnames: []string{}}, nil
		}}),
		tailscaleconnection.NewProvider(tailscaleconnection.Options{NewLocalAPI: func(string) tailscaleconnection.LocalAPI { return connectionTailLocal{} }}),
		thirdConnectionProvider{detect: true},
	}
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

type connectionCase struct {
	kind           string
	config         string
	secrets        string
	optionalSecret string
}

func connectionCases() []connectionCase {
	return []connectionCase{
		{"cloudflare", `{}`, `{"tunnel_token":"` + testTunnelToken("acct1", "tun1") + `","api_token":"credential-value"}`, "api_token"},
		{"tailscale", `{"tailnet":"example.com"}`, `{"oauth_client_secret":"credential-value"}`, "oauth_client_secret"},
		{"third", `{}`, `{"third_key":"credential-value"}`, "third_key"},
	}
}

func createTestConnection(t *testing.T, send func(string, string, string) (int, string), candidate connectionCase, name string) string {
	t.Helper()
	body := fmt.Sprintf(`{"type":%q,"name":%q,"config":%s,"secrets":%s}`, candidate.kind, name, candidate.config, candidate.secrets)
	code, response := send(http.MethodPost, "/api/v1/connections", body)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, response)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.ID, "conn") {
		t.Fatalf("server ID missing: %s", response)
	}
	return result.ID
}

func TestShouldServeConnectionCRUDForEveryRegisteredType(t *testing.T) {
	for _, candidate := range connectionCases() {
		t.Run(candidate.kind, func(t *testing.T) {
			api, server := newNetworkTestAPI(t)
			hub := newFakeConnectionControl()
			api.SetConnections(hub, testConnectionRegistry(t), testSecretBox(t))
			api.SetHostCommands(true)
			send := func(method, path, body string) (int, string) { return statusAndBody(t, server, method, path, body) }
			id := createTestConnection(t, send, candidate, "one")
			second := createTestConnection(t, send, candidate, "two")
			if second == id {
				t.Fatal("same-type connections shared identity")
			}
			code, response := send(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":%q,"name":"one","config":%s,"secrets":%s}`, candidate.kind, candidate.config, candidate.secrets))
			if code != http.StatusConflict {
				t.Fatalf("duplicate name: %d %s", code, response)
			}
			for _, path := range []string{"/api/v1/connections", "/api/v1/connections/" + id} {
				code, response := send(http.MethodGet, path, "")
				if code != http.StatusOK || strings.Contains(response, "credential-value") || strings.Contains(response, "hidden") || !strings.Contains(response, `"has_`+candidate.optionalSecret+`":true`) {
					t.Fatalf("read: %d %s", code, response)
				}
			}
			if err := api.data.UpdateConnectionState(context.Background(), defaultOrgID, id, json.RawMessage(`{"private":"provider-owned"}`)); err != nil {
				t.Fatal(err)
			}
			code, response = send(http.MethodPut, "/api/v1/connections/"+id, fmt.Sprintf(`{"name":"renamed","config":%s}`, candidate.config))
			if code != http.StatusOK || !strings.Contains(response, `"has_`+candidate.optionalSecret+`":true`) {
				t.Fatalf("retention: %d %s", code, response)
			}
			record, err := api.data.GetConnection(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if string(record.State) != `{"private":"provider-owned"}` {
				t.Fatalf("private state changed: %s", record.State)
			}
			plain, err := api.secretBox.Open(record.SecretsEnc, connections.SecretContext(defaultOrgID, id))
			if err != nil || !strings.Contains(string(plain), "credential-value") {
				t.Fatalf("sealed secret: %s %v", plain, err)
			}
			if _, err := api.secretBox.Open(record.SecretsEnc, connections.SecretContext("other", id)); err == nil {
				t.Fatal("secrets not org-bound")
			}
			if _, err := api.secretBox.Open(record.SecretsEnc, connections.SecretContext(defaultOrgID, second)); err == nil {
				t.Fatal("secrets not ID-bound")
			}
			code, response = send(http.MethodPut, "/api/v1/connections/"+id, fmt.Sprintf(`{"name":"renamed","config":%s,"secrets":{"%s":""}}`, candidate.config, candidate.optionalSecret))
			if code != http.StatusOK || !strings.Contains(response, `"has_`+candidate.optionalSecret+`":false`) {
				t.Fatalf("clearing: %d %s", code, response)
			}
			code, response = send(http.MethodPut, "/api/v1/connections/"+id, `{"type":"different","name":"x","config":{}}`)
			if code != http.StatusBadRequest || !strings.Contains(response, "immutable") {
				t.Fatalf("immutable: %d %s", code, response)
			}
			code, response = send(http.MethodPost, "/api/v1/connections/"+id+"/sync", "")
			if code != http.StatusOK {
				t.Fatalf("sync: %d %s", code, response)
			}
			if code, response = send(http.MethodDelete, "/api/v1/connections/"+id, ""); code != http.StatusNoContent {
				t.Fatalf("delete: %d %s", code, response)
			}
			if code, response = send(http.MethodGet, "/api/v1/connections/"+id, ""); code != http.StatusNotFound {
				t.Fatalf("deleted read: %d %s", code, response)
			}
		})
	}
}

func TestShouldRejectUnknownConnectionInputWithoutCredentialEcho(t *testing.T) {
	api, server := newNetworkTestAPI(t)
	api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
	for _, body := range []string{
		`{"type":"unknown","name":"x","config":{}}`,
		`{"type":"third","name":"x","config":{"unknown":"credential-value"}}`,
		`{"type":"third","name":"x","config":{},"secrets":{"unknown":"credential-value"}}`,
		`{"type":"third","name":"x","config":{"location":"invalid"},"secrets":{"third_key":"credential-value"}}`,
		`{"type":"third","name":"x","config":{},"state":{"secret":"credential-value"}}`,
		`{"type":"third","name":"x","config":{},"id":"client-ID"}`,
		`{"type":"third","name":"x","config":{},"org_id":"other"}`,
		`{"type":"third","name":"x","config":{"location":"one","location":"two"}}`,
		`{"type":"third","name":"x","config":{},"secrets":{"third_key":null}}`,
		`{"type":"third","name":"x","config":{}} {}`,
		`{"type":"third","name":"x","config":null}`,
	} {
		code, response := statusAndBody(t, server, http.MethodPost, "/api/v1/connections", body)
		if code != http.StatusBadRequest || strings.Contains(response, "credential-value") {
			t.Fatalf("%s: %d %s", body, code, response)
		}
	}
}

func TestShouldSanitizeDetectForEveryTypeAndRejectUnsupportedDetect(t *testing.T) {
	api, server := newNetworkTestAPI(t)
	api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
	api.SetHostCommands(true)
	for _, candidate := range connectionCases() {
		input := `{}`
		if candidate.kind == "third" {
			input = `{"third_key":"credential-value"}`
		}
		code, response := statusAndBody(t, server, http.MethodPost, "/api/v1/connections/detect", fmt.Sprintf(`{"type":%q,"input":%s}`, candidate.kind, input))
		if code != http.StatusOK || strings.Contains(response, "credential-value") || strings.Contains(response, `"managed"`) {
			t.Fatalf("%s: %d %s", candidate.kind, code, response)
		}
	}
	for _, body := range []string{`{"type":"unknown","input":{}}`, `{"type":"third","input":{"unknown":"credential-value"}}`, `{"type":"third","input":{"location":"invalid","third_key":"credential-value"}}`, `{"type":"third","input":{"state":"credential-value"}}`} {
		code, response := statusAndBody(t, server, http.MethodPost, "/api/v1/connections/detect", body)
		if code != http.StatusBadRequest || strings.Contains(response, "credential-value") {
			t.Fatalf("detect validation: %d %s", code, response)
		}
	}
	registry := connections.NewRegistry()
	if err := registry.Register(thirdConnectionProvider{}); err != nil {
		t.Fatal(err)
	}
	api.SetConnections(newFakeConnectionControl(), registry, testSecretBox(t))
	code, response := statusAndBody(t, server, http.MethodPost, "/api/v1/connections/detect", `{"type":"third","input":{}}`)
	if code != http.StatusBadRequest || !strings.Contains(response, "does not support Detect") {
		t.Fatalf("unsupported: %d %s", code, response)
	}
}

func TestShouldKeepCommittedConnectionOnRuntimeFailure(t *testing.T) {
	for _, reloadFailure := range []bool{true, false} {
		t.Run(fmt.Sprint(reloadFailure), func(t *testing.T) {
			api, server := newNetworkTestAPI(t)
			hub := newFakeConnectionControl()
			if reloadFailure {
				hub.reloadError = errors.New("credential-value")
			} else {
				hub.syncError = errors.New("credential-value")
			}
			api.SetConnections(hub, testConnectionRegistry(t), testSecretBox(t))
			code, response := statusAndBody(t, server, http.MethodPost, "/api/v1/connections", `{"type":"third","name":"saved","config":{},"secrets":{"third_key":"credential-value"}}`)
			if code != http.StatusCreated || !strings.Contains(response, "Connection saved") || strings.Contains(response, "credential-value") {
				t.Fatalf("committed failure: %d %s", code, response)
			}
			var view struct {
				ID     string             `json:"id"`
				Status connections.Status `json:"status"`
			}
			_ = json.Unmarshal([]byte(response), &view)
			record, err := api.data.GetConnection(context.Background(), view.ID)
			if err != nil || record.Name != "saved" || view.Status.LastError == "" || view.Status.Health == "up" {
				t.Fatalf("saved row/status: %+v %+v %v", record, view.Status, err)
			}
		})
	}
}

func TestShouldBlockDeletionWithAtomicSameOrgReferenceDetails(t *testing.T) {
	for _, kind := range []string{"service", "endpoint", "alias"} {
		t.Run(kind, func(t *testing.T) {
			api, server := newNetworkTestAPI(t)
			api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
			send := func(method, path, body string) (int, string) { return statusAndBody(t, server, method, path, body) }
			id := createTestConnection(t, send, connectionCases()[2], "referenced")
			ctx := context.Background()
			if kind == "service" {
				err := api.data.CreateService(ctx, &store.Service{ID: "svc-ref", OrgID: defaultOrgID, Name: "same-org-reference", Driver: "command", Config: json.RawMessage(`{}`), ConnectionIDs: []string{id}, CreatedAt: time.Now(), UpdatedAt: time.Now()})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				endpoint := &store.Endpoint{ID: "ep-ref", AgentID: proxy.AgentID, OrgID: defaultOrgID, Subdomain: "same-org-reference", Kind: store.KindHTTP, Method: store.MethodProxy, CreatedAt: time.Now(), Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: id, Hostname: "same-org.example.org"}}}}
				if err := api.data.CreateEndpoint(ctx, endpoint); err != nil {
					t.Fatal(err)
				}
				if kind == "alias" {
					if err := api.data.ReplaceConnectionAliases(ctx, defaultOrgID, id, []store.EndpointAlias{{EndpointID: endpoint.ID, ConnectionID: id, OrgID: defaultOrgID, Hostname: "same-org.example.org"}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			code, response := send(http.MethodDelete, "/api/v1/connections/"+id, "")
			if code != http.StatusConflict || !strings.Contains(response, "same-org-reference") || !strings.Contains(response, `"references"`) {
				t.Fatalf("guard: %d %s", code, response)
			}
			if _, err := api.data.GetConnection(ctx, id); err != nil {
				t.Fatal("referenced connection was deleted", err)
			}
		})
	}
}

func TestShouldEnforceConnectionRolesForEveryProvider(t *testing.T) {
	for _, candidate := range connectionCases() {
		t.Run(candidate.kind, func(t *testing.T) {
			f := newTenantFixture(t)
			f.api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
			f.api.SetHostCommands(true)
			admin := inviteAndRegister(t, f.srv, f.alice, "admin@example.com", store.RoleAdmin)
			member := inviteAndRegister(t, f.srv, f.alice, "member@example.com", store.RoleMember)
			for _, role := range []struct {
				name   string
				client *http.Client
				manage bool
				detect bool
			}{
				{"owner", f.alice, true, true}, {"admin", admin, true, candidate.kind == "third"}, {"member", member, false, false},
			} {
				body := fmt.Sprintf(`{"type":%q,"name":%q,"config":%s,"secrets":%s}`, candidate.kind, role.name, candidate.config, candidate.secrets)
				code, response := f.do(t, role.client, http.MethodPost, "/api/v1/connections", body)
				if !role.manage {
					if code != http.StatusForbidden {
						t.Fatalf("%s create: %d %s", role.name, code, response)
					}
					continue
				}
				if code != http.StatusCreated {
					t.Fatalf("%s create: %d %s", role.name, code, response)
				}
				var created struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal([]byte(response), &created)
				for _, reader := range []*http.Client{f.alice, admin, member} {
					for _, path := range []string{"/api/v1/connection-types", "/api/v1/connections", "/api/v1/connections/" + created.ID} {
						doc(t, reader, f.srv, http.MethodGet, path, "", http.StatusOK, nil)
					}
				}
				for _, method := range []string{http.MethodPut, http.MethodDelete} {
					doc(t, member, f.srv, method, "/api/v1/connections/"+created.ID, body, http.StatusForbidden, nil)
				}
				doc(t, member, f.srv, http.MethodPost, "/api/v1/connections/"+created.ID+"/sync", "", http.StatusForbidden, nil)
				doc(t, role.client, f.srv, http.MethodPut, "/api/v1/connections/"+created.ID, body, http.StatusOK, nil)
				doc(t, role.client, f.srv, http.MethodPost, "/api/v1/connections/"+created.ID+"/sync", "", http.StatusOK, nil)
				wantDetect := http.StatusForbidden
				if role.detect {
					wantDetect = http.StatusOK
				}
				doc(t, role.client, f.srv, http.MethodPost, "/api/v1/connections/detect", fmt.Sprintf(`{"type":%q,"input":{}}`, candidate.kind), wantDetect, nil)
				doc(t, role.client, f.srv, http.MethodDelete, "/api/v1/connections/"+created.ID, "", http.StatusNoContent, nil)
			}
			doc(t, member, f.srv, http.MethodPost, "/api/v1/connections/detect", fmt.Sprintf(`{"type":%q,"input":{}}`, candidate.kind), http.StatusForbidden, nil)
		})
	}
}

func TestShouldProtectOldAndProposedHostCommandConfigurations(t *testing.T) {
	f := newTenantFixture(t)
	f.api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
	f.api.SetHostCommands(true)
	admin := inviteAndRegister(t, f.srv, f.alice, "admin@example.com", store.RoleAdmin)
	body := `{"type":"third","name":"host-command","config":{"command":["true"]}}`
	var created struct {
		ID string `json:"id"`
	}
	doc(t, admin, f.srv, http.MethodPost, "/api/v1/connections", body, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections", body, http.StatusCreated, &created)
	path := "/api/v1/connections/" + created.ID
	doc(t, admin, f.srv, http.MethodPut, path, `{"name":"stripped","config":{}}`, http.StatusForbidden, nil)
	doc(t, admin, f.srv, http.MethodDelete, path, "", http.StatusForbidden, nil)
	doc(t, admin, f.srv, http.MethodPost, path+"/sync", "", http.StatusForbidden, nil)
	doc(t, admin, f.srv, http.MethodPost, "/api/v1/connections/detect", `{"type":"third","input":{"command":["true"]}}`, http.StatusForbidden, nil)
	f.api.SetHostCommands(false)
	doc(t, f.alice, f.srv, http.MethodPut, path, `{"name":"stripped","config":{}}`, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodDelete, path, "", http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections/detect", `{"type":"cloudflare","input":{}}`, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections/detect", `{"type":"tailscale","input":{}}`, http.StatusForbidden, nil)
	f.api.SetHostCommands(true)
	doc(t, f.alice, f.srv, http.MethodPut, path, `{"name":"stripped","config":{}}`, http.StatusOK, nil)
	doc(t, admin, f.srv, http.MethodDelete, path, "", http.StatusNoContent, nil)
	managed := `{"type":"cloudflare","name":"managed","config":{"connector":"managed"},"secrets":{"tunnel_token":"` + testTunnelToken("acct1", "tun1") + `"}}`
	doc(t, admin, f.srv, http.MethodPost, "/api/v1/connections", managed, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections", managed, http.StatusCreated, &created)
	doc(t, admin, f.srv, http.MethodPut, "/api/v1/connections/"+created.ID, `{"name":"stripped","config":{"connector":"external"}}`, http.StatusForbidden, nil)
	secretBody := `{"type":"third","name":"host-secret","config":{},"secrets":{"host_secret":"host-credential"}}`
	doc(t, admin, f.srv, http.MethodPost, "/api/v1/connections", secretBody, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections", secretBody, http.StatusCreated, &created)
	doc(t, admin, f.srv, http.MethodPut, "/api/v1/connections/"+created.ID, `{"name":"stripped","config":{},"secrets":{"host_secret":""}}`, http.StatusForbidden, nil)
	doc(t, admin, f.srv, http.MethodDelete, "/api/v1/connections/"+created.ID, "", http.StatusForbidden, nil)
}

func TestShouldHideEveryForeignConnectionIDFromAllRoles(t *testing.T) {
	f := newTenantFixture(t)
	hub := newFakeConnectionControl()
	f.api.SetConnections(hub, testConnectionRegistry(t), testSecretBox(t))
	f.api.SetHostCommands(true)
	admin := inviteAndRegister(t, f.srv, f.bob, "admin@example.com", store.RoleAdmin)
	member := inviteAndRegister(t, f.srv, f.bob, "member@example.com", store.RoleMember)
	for _, candidate := range connectionCases() {
		body := fmt.Sprintf(`{"type":%q,"name":%q,"config":%s,"secrets":%s}`, candidate.kind, "alice-"+candidate.kind, candidate.config, candidate.secrets)
		var created struct {
			ID string `json:"id"`
		}
		doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections", body, http.StatusCreated, &created)
		for _, client := range []*http.Client{f.bob, admin, member} {
			for _, probe := range []struct {
				method string
				suffix string
				body   string
			}{
				{http.MethodGet, "", ""}, {http.MethodPut, "", `{"name":"hijacked","config":{}}`}, {http.MethodDelete, "", ""}, {http.MethodPost, "/sync", ""},
			} {
				want := http.StatusNotFound
				if client == member && probe.method != http.MethodGet {
					want = http.StatusForbidden
				}
				code, response := f.do(t, client, probe.method, "/api/v1/connections/"+created.ID+probe.suffix+"?org_id="+f.orgA, probe.body)
				if code != want || strings.Contains(response, created.ID) || strings.Contains(response, "credential-value") || strings.Contains(response, "alice-") {
					t.Fatalf("foreign %s: %d %s", probe.method, code, response)
				}
			}
			code, response := f.do(t, client, http.MethodGet, "/api/v1/connections?org_id="+f.orgA, "")
			if code != http.StatusOK || strings.Contains(response, created.ID) {
				t.Fatalf("foreign list: %d %s", code, response)
			}
		}
	}
}

func TestShouldInvalidateConnectionAuthorizationCacheOnPolicyEdits(t *testing.T) {
	f := newTenantFixture(t)
	f.api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
	member := inviteAndRegister(t, f.srv, f.alice, "member@example.com", store.RoleMember)
	body := `{"type":"third","name":"member","config":{}}`
	doc(t, member, f.srv, http.MethodPost, "/api/v1/connections", body, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/policy", `{"matrix":{"owner":["policy:read","policy:write","endpoint:read","connection:manage"],"member":["endpoint:read","connection:manage"]}}`, http.StatusOK, nil)
	doc(t, member, f.srv, http.MethodPost, "/api/v1/connections", body, http.StatusCreated, nil)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/policy", `{"matrix":{"owner":["policy:read","policy:write","endpoint:read","connection:manage"],"member":["endpoint:read"]}}`, http.StatusOK, nil)
	doc(t, member, f.srv, http.MethodPost, "/api/v1/connections", `{"type":"third","name":"revoked","config":{}}`, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/policy", `{"matrix":{"member":["cloudflare:manage"]}}`, http.StatusBadRequest, nil)
}

func TestShouldPreserveAuthenticationCSRFLimitsAndSensitiveOutput(t *testing.T) {
	f := newTenantFixture(t)
	f.api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
	code, response := f.do(t, http.DefaultClient, http.MethodGet, "/api/v1/connection-types", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated descriptors: %d %s", code, response)
	}
	request, err := http.NewRequest(http.MethodPost, f.srv.URL+"/api/v1/connections", strings.NewReader(`{"type":"third","name":"x","config":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://foreign.example")
	result, err := f.alice.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	result.Body.Close()
	if result.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin create: %d", result.StatusCode)
	}
	body := `{"type":"third","name":"x","config":{"location":"` + strings.Repeat("x", 1<<20) + `"}}`
	code, response = f.do(t, f.alice, http.MethodPost, "/api/v1/connections", body)
	if code != http.StatusBadRequest {
		t.Fatalf("request limit: %d %s", code, response)
	}
	body = `{"type":"third","type":"third","name":"x","config":{}}`
	code, response = f.do(t, f.alice, http.MethodPost, "/api/v1/connections", body)
	if code != http.StatusBadRequest {
		t.Fatalf("duplicate envelope: %d %s", code, response)
	}
	body = `{"type":"third","name":"sensitive","config":{"sensitive":"sensitive-value"},"secrets":{"third_key":"credential-value"}}`
	code, response = f.do(t, f.alice, http.MethodPost, "/api/v1/connections", body)
	if code != http.StatusCreated || strings.Contains(response, "sensitive-value") || strings.Contains(response, "credential-value") {
		t.Fatalf("sensitive config: %d %s", code, response)
	}
}

type racingConnectionData struct {
	store.DataStore
	change func(context.Context, string) error
}

func (data racingConnectionData) UpdateConnectionIfCurrent(ctx context.Context, record, expected *store.Connection) error {
	if err := data.change(ctx, expected.ID); err != nil {
		return err
	}
	return data.DataStore.UpdateConnectionIfCurrent(ctx, record, expected)
}

func (data racingConnectionData) DeleteConnectionIfCurrent(ctx context.Context, expected *store.Connection) error {
	if err := data.change(ctx, expected.ID); err != nil {
		return err
	}
	return data.DataStore.DeleteConnectionIfCurrent(ctx, expected)
}

func TestShouldRejectStaleAuthorizedUpdateAndDeleteWithoutCredentialEcho(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			f := newTenantFixture(t)
			f.api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
			f.api.SetHostCommands(true)
			admin := inviteAndRegister(t, f.srv, f.alice, "admin@example.com", store.RoleAdmin)
			var created struct {
				ID string `json:"id"`
			}
			doc(t, admin, f.srv, http.MethodPost, "/api/v1/connections", `{"type":"third","name":"initial","config":{},"secrets":{"third_key":"credential-value"}}`, http.StatusCreated, &created)
			underlying := f.api.data
			changes := 0
			f.api.data = racingConnectionData{DataStore: underlying, change: func(ctx context.Context, id string) error {
				changes++
				row, err := underlying.GetConnection(ctx, id)
				if err != nil {
					return err
				}
				row.Config = json.RawMessage(`{"command":["owner-command"]}`)
				row.Name = "owner-changed"
				return underlying.UpdateConnection(ctx, row)
			}}
			body := `{"name":"stripped","config":{}}`
			code, response := f.do(t, admin, method, "/api/v1/connections/"+created.ID, body)
			if code != http.StatusConflict || strings.Contains(response, "credential-value") {
				t.Fatalf("stale %s: %d %s", method, code, response)
			}
			row, err := underlying.GetConnection(context.Background(), created.ID)
			if err != nil || row.Name != "owner-changed" || !strings.Contains(string(row.Config), "owner-command") {
				t.Fatalf("unchecked mutation won: %+v %v", row, err)
			}
			code, response = f.do(t, f.bob, method, "/api/v1/connections/"+created.ID+"?org_id="+f.orgA, body)
			if code != http.StatusNotFound || changes != 1 || strings.Contains(response, "owner-changed") || strings.Contains(response, created.ID) {
				t.Fatalf("foreign mutation: %d %s changes=%d", code, response, changes)
			}
		})
	}
}

func TestShouldRejectStaleSyncSnapshotWithoutUnauthorizedRecovery(t *testing.T) {
	f := newTenantFixture(t)
	hub := newFakeConnectionControl()
	f.api.SetConnections(hub, testConnectionRegistry(t), testSecretBox(t))
	admin := inviteAndRegister(t, f.srv, f.alice, "admin@example.com", store.RoleAdmin)
	var created struct {
		ID string `json:"id"`
	}
	doc(t, admin, f.srv, http.MethodPost, "/api/v1/connections", `{"type":"third","name":"initial","config":{}}`, http.StatusCreated, &created)
	hub.syncError = store.ErrConflict
	before := hub.reloads[created.ID]
	code, response := f.do(t, admin, http.MethodPost, "/api/v1/connections/"+created.ID+"/sync", "")
	if code != http.StatusConflict || hub.reloads[created.ID] != before || strings.Contains(response, created.ID) {
		t.Fatalf("stale sync: %d %s", code, response)
	}
}

func TestShouldMapConnectionHostnameChangesToStatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		suffix string
		body   string
		err    error
		code   int
		want   string
	}{
		{"add", http.MethodPost, "/hostnames", `{"hostname":"vault.example.com","service":"http://localhost:8000"}`, nil, http.StatusCreated, "add vault.example.com http://localhost:8000"},
		{"remove", http.MethodDelete, "/hostnames/vault.example.com", "", nil, http.StatusOK, "remove vault.example.com"},
		{"invalid", http.MethodPost, "/hostnames", `{"hostname":"x","service":"ftp://x"}`, &connections.HostnameError{Kind: connections.HostnameInvalid, Message: "bad service"}, http.StatusBadRequest, "bad service"},
		{"conflict", http.MethodPost, "/hostnames", `{"hostname":"x","service":"http://x"}`, &connections.HostnameError{Kind: connections.HostnameConflict, Message: "already routed"}, http.StatusConflict, "already routed"},
		{"in use", http.MethodDelete, "/hostnames/x", "", &connections.HostnameError{Kind: connections.HostnameInUse, Message: "published", EndpointIDs: []string{"ep1"}}, http.StatusConflict, `"endpoint_ids":["ep1"]`},
		{"not found", http.MethodDelete, "/hostnames/x", "", &connections.HostnameError{Kind: connections.HostnameNotFound, Message: "not on tunnel"}, http.StatusNotFound, "not on tunnel"},
		{"unsupported", http.MethodPost, "/hostnames", `{"hostname":"x","service":"http://x"}`, &connections.HostnameError{Kind: connections.HostnameUnsupported, Message: "add an API token"}, http.StatusConflict, "add an API token"},
		{"stale", http.MethodDelete, "/hostnames/x", "", store.ErrConflict, http.StatusConflict, "connection changed"},
		{"not running", http.MethodDelete, "/hostnames/x", "", connections.ErrNotConnected, http.StatusServiceUnavailable, "not running"},
		{"provider", http.MethodDelete, "/hostnames/x", "", errors.New("credential-value"), http.StatusBadGateway, "API token permissions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, server := newNetworkTestAPI(t)
			hub := newFakeConnectionControl()
			api.SetConnections(hub, testConnectionRegistry(t), testSecretBox(t))
			send := func(method, path, body string) (int, string) { return statusAndBody(t, server, method, path, body) }
			id := createTestConnection(t, send, connectionCases()[0], "tunnel")
			hub.hostError = tc.err
			before := hub.syncs[id]
			code, response := send(tc.method, "/api/v1/connections/"+id+tc.suffix, tc.body)
			wantBody := tc.want
			if tc.err == nil {
				wantBody = `"id":"` + id + `"`
			}
			if code != tc.code || !strings.Contains(response, wantBody) || strings.Contains(response, "credential-value") {
				t.Fatalf("%d %s", code, response)
			}
			if tc.err == nil && (len(hub.hostnames) != 1 || hub.hostnames[0] != tc.want || hub.syncs[id] != before+1) {
				t.Fatalf("hostnames=%v syncs=%d", hub.hostnames, hub.syncs[id]-before)
			}
			if tc.err != nil && hub.syncs[id] != before {
				t.Fatalf("failed change synced: %d", hub.syncs[id]-before)
			}
		})
	}
}

func TestShouldForbidHostnameChangesWithoutConnectionManage(t *testing.T) {
	f := newTenantFixture(t)
	hub := newFakeConnectionControl()
	f.api.SetConnections(hub, testConnectionRegistry(t), testSecretBox(t))
	var created struct {
		ID string `json:"id"`
	}
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections", `{"type":"third","name":"tunnel","config":{}}`, http.StatusCreated, &created)
	member := inviteAndRegister(t, f.srv, f.alice, "member@example.com", store.RoleMember)
	doc(t, member, f.srv, http.MethodPost, "/api/v1/connections/"+created.ID+"/hostnames", `{"hostname":"vault.example.com","service":"http://localhost:8000"}`, http.StatusForbidden, nil)
	doc(t, member, f.srv, http.MethodDelete, "/api/v1/connections/"+created.ID+"/hostnames/vault.example.com", "", http.StatusForbidden, nil)
	if len(hub.hostnames) != 0 {
		t.Fatalf("forbidden change reached the runtime: %v", hub.hostnames)
	}
}

func TestShouldSanitizeStoredSecretsAcrossStatusSyncAndReferenceErrors(t *testing.T) {
	api, server := newNetworkTestAPI(t)
	hub := newFakeConnectionControl()
	api.SetConnections(hub, testConnectionRegistry(t), testSecretBox(t))
	send := func(method, path, body string) (int, string) { return statusAndBody(t, server, method, path, body) }
	id := createTestConnection(t, send, connectionCases()[2], "sanitized")
	hub.statuses[id] = connections.Status{Health: "up", LastError: "credential-value", Details: map[string]any{"credential": "credential-value", "summary": "credential-value"}, Hostnames: []connections.HostStatus{{Host: "credential-value.example", Detail: "credential-value", Fix: "credential-value"}}}
	for _, probe := range []struct {
		method string
		path   string
		code   int
	}{
		{http.MethodGet, "/api/v1/connections", http.StatusOK}, {http.MethodGet, "/api/v1/connections/" + id, http.StatusOK}, {http.MethodPost, "/api/v1/connections/" + id + "/sync", http.StatusOK},
	} {
		code, response := send(probe.method, probe.path, "")
		if code != probe.code || strings.Contains(response, "credential-value") {
			t.Fatalf("status leak: %d %s", code, response)
		}
	}
	hub.syncError = errors.New("credential-value")
	code, response := send(http.MethodPost, "/api/v1/connections/"+id+"/sync", "")
	if code != http.StatusBadGateway || strings.Contains(response, "credential-value") || !strings.Contains(response, "Retry sync") {
		t.Fatalf("sync leak: %d %s", code, response)
	}
	if err := api.data.CreateService(context.Background(), &store.Service{ID: "svc-secret-name", OrgID: defaultOrgID, Name: "credential-value", Driver: "command", Config: json.RawMessage(`{}`), ConnectionIDs: []string{id}, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	code, response = send(http.MethodDelete, "/api/v1/connections/"+id, "")
	if code != http.StatusConflict || strings.Contains(response, "credential-value") {
		t.Fatalf("reference error leak: %d %s", code, response)
	}
}

func TestShouldRemoveEveryOldCloudflareRoute(t *testing.T) {
	_, server := newNetworkTestAPI(t)
	for _, probe := range []struct {
		method string
		suffix string
	}{
		{http.MethodGet, ""}, {http.MethodPut, ""}, {http.MethodDelete, ""}, {http.MethodPost, "/detect"}, {http.MethodPost, "/sync"},
	} {
		code, response := statusAndBody(t, server, probe.method, "/api/v1/cloudflare"+probe.suffix, "")
		if code != http.StatusNotFound {
			t.Fatalf("old %s%s: %d %s", probe.method, probe.suffix, code, response)
		}
	}
}

func TestShouldRemoveOldCloudflareRoute(t *testing.T) {
	_, server := newNetworkTestAPI(t)
	code, body := statusAndBody(t, server, http.MethodGet, "/api/v1/cloudflare", "")
	if code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", code, body)
	}
}
