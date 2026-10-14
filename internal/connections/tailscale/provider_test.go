package tailscaleconnection

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/views"
)

type redirectTransport struct{ base *url.URL }

func tags(values ...string) *views.Slice[string] { result := views.SliceOf(values); return &result }

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	target := *req.URL
	target.Scheme, target.Host = r.base.Scheme, r.base.Host
	copy.URL = &target
	return http.DefaultTransport.RoundTrip(copy)
}

type fakeTailscale struct {
	mu                                       sync.Mutex
	server                                   *httptest.Server
	status                                   *ipnstate.Status
	serve                                    *ipn.ServeConfig
	prefs                                    *ipn.Prefs
	services                                 map[string]Service
	serviceHosts                             map[string][]ServiceHost
	writes, tokens, servePosts, prefsPatches int
	conflicts                                int
	apiFailure, oauthFailure, localFailure   bool
	nodeMismatch                             bool
	apiPutFailure                            bool
	apiReadEdit                              bool
	servePostFailure                         bool
	prefsPatchFailure                        bool
	prefsReads, advertiseAtPrefsRead         int
}

func newFakeTailscale(t *testing.T) *fakeTailscale {
	t.Helper()
	f := &fakeTailscale{status: &ipnstate.Status{Version: "1.102.2", BackendState: "Running", Self: &ipnstate.PeerStatus{ID: "node-one", NodeID: 123, Tags: tags("tag:host"), CapMap: tailcfg.NodeCapMap{}}, CurrentTailnet: &ipnstate.TailnetStatus{Name: "example.com", MagicDNSSuffix: "test.ts.net"}}, serve: &ipn.ServeConfig{Services: map[tailcfg.ServiceName]*ipn.ServiceConfig{}}, prefs: &ipn.Prefs{AdvertiseServices: []string{"svc:foreign"}, WantRunning: true}, services: map[string]Service{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTailscale) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		f.writes++
	}
	if strings.HasPrefix(r.URL.Path, "/localapi/") && f.localFailure {
		http.Error(w, "local secret", 503)
		return
	}
	switch r.URL.Path {
	case "/localapi/v0/status":
		if r.URL.Query().Get("peers") != "false" {
			http.Error(w, "peers should be excluded", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(f.status)
	case "/localapi/v0/serve-config":
		w.Header().Set("ETag", fmt.Sprintf("\"%d\"", f.servePosts))
		if r.Method == http.MethodPost {
			f.servePosts++
			if f.servePostFailure {
				http.Error(w, "serve failed", 500)
				return
			}
			if f.conflicts > 0 {
				f.conflicts--
				f.serve.Services["svc:late-foreign"] = serviceConfig("svc:late-foreign", "test.ts.net", "http://127.0.0.1:9999")
				http.Error(w, "etag mismatch", 412)
				return
			}
			if r.Header.Get("If-Match") != fmt.Sprintf("\"%d\"", f.servePosts-1) {
				http.Error(w, "missing etag", 412)
				return
			}
			var next ipn.ServeConfig
			if json.NewDecoder(r.Body).Decode(&next) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			f.serve = &next
		}
		_ = json.NewEncoder(w).Encode(f.serve)
	case "/localapi/v0/prefs":
		if r.Method == http.MethodGet {
			f.prefsReads++
			if f.advertiseAtPrefsRead > 0 && f.prefsReads == f.advertiseAtPrefsRead {
				f.prefs.AdvertiseServices = append(f.prefs.AdvertiseServices, "svc:notes")
			}
		}
		if r.Method == http.MethodPatch {
			f.prefsPatches++
			if f.prefsPatchFailure {
				http.Error(w, "advertisement failed", 500)
				return
			}
			var masked ipn.MaskedPrefs
			if json.NewDecoder(r.Body).Decode(&masked) != nil || !masked.AdvertiseServicesSet {
				http.Error(w, "invalid", 400)
				return
			}
			raw, _ := json.Marshal(masked)
			var masks map[string]any
			_ = json.Unmarshal(raw, &masks)
			for key, value := range masks {
				if strings.HasSuffix(key, "Set") && key != "AdvertiseServicesSet" && value != false {
					http.Error(w, "unrelated prefs changed", 400)
					return
				}
			}
			f.prefs.AdvertiseServices = masked.AdvertiseServices
		}
		_ = json.NewEncoder(w).Encode(f.prefs)
	case "/api/v2/oauth/token":
		f.tokens++
		if f.oauthFailure {
			http.Error(w, strings.Repeat("secret-client-secret ", 100000), 401)
			return
		}
		if r.ParseForm() != nil || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_secret") != "secret-client-secret" {
			http.Error(w, "invalid credentials", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "secret-token", "token_type": "Bearer", "expires_in": 3600})
	default:
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "missing auth", 401)
			return
		}
		if f.apiFailure {
			http.Error(w, "secret-token secret-client-secret", 403)
			return
		}
		if r.URL.Path == "/api/v2/tailnet/example.com/devices" {
			node := "node-one"
			if f.nodeMismatch {
				node = "another-node"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"devices": []map[string]string{{"id": "999", "nodeId": node}}})
			return
		}
		prefix := "/api/v2/tailnet/example.com/services/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "wrong tailnet or path", 404)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if strings.HasSuffix(name, "/devices") {
			name = strings.TrimSuffix(name, "/devices")
			hosts := []ServiceHost{}
			if configured, present := f.serviceHosts[name]; present {
				hosts = configured
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"hosts": hosts})
			return
		}
		switch r.Method {
		case http.MethodGet:
			service, exists := f.services[name]
			if !exists {
				http.Error(w, "absent", 404)
				return
			}
			if f.apiReadEdit {
				service.Comment = "external edit"
			}
			_ = json.NewEncoder(w).Encode(service)
		case http.MethodPut:
			if f.apiPutFailure {
				http.Error(w, "creation failed", 500)
				return
			}
			var service Service
			if json.NewDecoder(r.Body).Decode(&service) != nil || service.Name != name {
				http.Error(w, "bad service", 400)
				return
			}
			if existing, present := f.services[name]; present && !slices.Equal(existing.Addrs, service.Addrs) {
				http.Error(w, "existing allocated addresses required", 400)
				return
			}
			service.Addrs = []string{"100.100.100.42", "fd7a:115c:a1e0::42"}
			f.services[name] = service
			_, _ = w.Write([]byte(`{}`))
		case http.MethodDelete:
			delete(f.services, name)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected method", 400)
		}
	}
}

func (f *fakeTailscale) local(socket string) LocalAPI {
	target, _ := url.Parse(f.server.URL)
	return NewLocalAPI(socket, redirectTransport{target})
}

type endpointData struct {
	store.DataStore
	endpoints []*store.Endpoint
	path      string
}

func (d *endpointData) ListEndpointsByOrg(_ context.Context, org string) ([]*store.Endpoint, error) {
	var result []*store.Endpoint
	for _, endpoint := range d.endpoints {
		if endpoint.OrgID == org {
			result = append(result, endpoint)
		}
	}
	return result, nil
}

func newFixture(t *testing.T, level string) (*fakeTailscale, connections.Provider, *endpointData, connections.Config, connections.Instance) {
	t.Helper()
	fake := newFakeTailscale(t)
	path := filepath.Join(t.TempDir(), "connections.db")
	data, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	wrapped := &endpointData{DataStore: data, path: path, endpoints: []*store.Endpoint{{ID: "ep-notes", OrgID: "org", Domain: "notes.example.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "conn"}}}}}}
	ctx := context.Background()
	if err := data.CreateOrg(ctx, &store.Org{ID: "org", Name: "org", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	config := connections.Config{Record: store.Connection{ID: "conn", OrgID: "org", Type: "tailscale", Name: "TS", Config: json.RawMessage(fmt.Sprintf(`{"level":%q,"socket":%q,"tailnet":"example.com","service_tags":["tag:apps"]}`, level, filepath.Join(t.TempDir(), "tailscale.sock"))), CreatedAt: time.Now(), UpdatedAt: time.Now()}, Secrets: map[string]string{"oauth_client_id": "client-id", "oauth_client_secret": "secret-client-secret"}}
	if err := data.CreateConnection(ctx, &config.Record); err != nil {
		t.Fatal(err)
	}
	provider := NewProvider(Options{NewLocalAPI: fake.local, APIBaseURL: fake.server.URL})
	instance, err := provider.Open(config, connections.Deps{Data: wrapped, Origin: "http://127.0.0.1:8090"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(instance.Close)
	return fake, provider, wrapped, config, instance
}

func TestShouldObserveImportedWithZeroWrites(t *testing.T) {
	fake, _, _, _, instance := newFixture(t, "imported")
	fake.serve.Services["svc:notes"] = serviceConfig("svc:notes", "test.ts.net", "http://127.0.0.1:8090")
	fake.prefs.AdvertiseServices = append(fake.prefs.AdvertiseServices, "svc:notes")
	fake.status.Self.CapMap[tailcfg.NodeAttrServiceHost] = []tailcfg.RawMessage{`{"svc:notes":["100.100.100.42"]}`}
	status, err := instance.Sync(context.Background())
	if err != nil || len(status.Hostnames) != 1 || status.Hostnames[0].State != "published" || fake.writes != 0 || fake.tokens != 0 {
		t.Fatalf("status=%+v err=%v writes=%d", status, err, fake.writes)
	}
	instance.Close()
	if fake.writes != 0 {
		t.Fatal("Close wrote configuration")
	}
}

func TestShouldManageHTTPSPreserveForeignResourcesAndPersistOwnership(t *testing.T) {
	fake, provider, data, config, instance := newFixture(t, "managed")
	fake.serve.TCP = map[uint16]*ipn.TCPPortHandler{8443: {TCPForward: "127.0.0.1:9999"}}
	fake.serve.AllowFunnel = map[ipn.HostPort]bool{"foreign.test:443": true}
	fake.serve.Foreground = map[string]*ipn.ServeConfig{"foreign": {TCP: map[uint16]*ipn.TCPPortHandler{8080: {HTTP: true}}}}
	fake.serve.Services["svc:foreign"] = serviceConfig("svc:foreign", "test.ts.net", "http://127.0.0.1:9999")
	foreign := fake.serve.Clone()
	fake.services["svc:foreign"] = Service{Name: "svc:foreign", Comment: "foreign"}
	status, err := instance.Sync(context.Background())
	if err != nil || status.Hostnames[0].State != "pending" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if !reflect.DeepEqual(fake.serve.Services["svc:notes"], serviceConfig("svc:notes", "test.ts.net", "http://127.0.0.1:8090")) || !reflect.DeepEqual(fake.serve.TCP, foreign.TCP) || !reflect.DeepEqual(fake.serve.AllowFunnel, foreign.AllowFunnel) || !reflect.DeepEqual(fake.serve.Foreground, foreign.Foreground) || !reflect.DeepEqual(fake.serve.Services["svc:foreign"], foreign.Services["svc:foreign"]) || !slices.Contains(fake.prefs.AdvertiseServices, "svc:foreign") || !fake.prefs.WantRunning {
		t.Fatal("foreign resources changed")
	}
	record, err := data.GetConnection(context.Background(), config.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	var state ownershipState
	if json.Unmarshal(record.State, &state) != nil || state.OwnedServices["svc:notes"] != "ep-notes" || state.API["svc:notes"] == "" || state.Serve["svc:notes"] == "" || !state.Advertised["svc:notes"] {
		t.Fatalf("state=%s", record.State)
	}
	instance.Close()
	if err := data.DataStore.(*sqlite.Store).Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(data.path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data.DataStore = database
	before := fake.writes
	config.Record.State = nil
	reopened, err := provider.Open(config, connections.Deps{Data: data, Origin: "http://127.0.0.1:8090"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fake.status.Self.CapMap[tailcfg.NodeAttrServiceHost] = []tailcfg.RawMessage{`{"svc:notes":["100.100.100.42"]}`}
	status, err = reopened.Sync(context.Background())
	if err != nil || status.Hostnames[0].State != "published" || fake.writes != before+1 {
		t.Fatalf("reopen=%+v err=%v writes=%d/%d", status, err, fake.writes, before)
	}
	data.endpoints[0].Policy.Access = nil
	status, err = reopened.Sync(context.Background())
	if err != nil || len(status.Hostnames) != 0 || fake.services["svc:notes"].Name != "" || fake.serve.Services["svc:notes"] != nil || slices.Contains(fake.prefs.AdvertiseServices, "svc:notes") || fake.services["svc:foreign"].Comment != "foreign" {
		t.Fatalf("cleanup=%+v err=%v", status, err)
	}
	if fake.tokens != 2 {
		t.Fatalf("token not cached: %d", fake.tokens)
	}
}

func TestShouldSerializeTwoConnectionsAndPreserveEachService(t *testing.T) {
	fake, provider, data, config, first := newFixture(t, "managed")
	secondConfig := config
	secondConfig.Record.ID, secondConfig.Record.Name = "second", "Second"
	secondConfig.Record.Config = json.RawMessage(strings.Replace(string(config.Record.Config), `"example.com"`, `"-"`, 1))
	if err := data.CreateConnection(context.Background(), &secondConfig.Record); err != nil {
		t.Fatal(err)
	}
	data.endpoints = append(data.endpoints, &store.Endpoint{ID: "ep-wiki", OrgID: "org", Domain: "wiki.example.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "second"}}}})
	second, err := provider.Open(secondConfig, connections.Deps{Data: data, Origin: "http://127.0.0.1:8090"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []connections.Instance{first, second} {
		group.Add(1)
		go func(instance connections.Instance) {
			defer group.Done()
			_, err := instance.Sync(context.Background())
			results <- err
		}(instance)
	}
	group.Wait()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if fake.serve.Services["svc:notes"] == nil || fake.serve.Services["svc:wiki"] == nil || !slices.Contains(fake.prefs.AdvertiseServices, "svc:notes") || !slices.Contains(fake.prefs.AdvertiseServices, "svc:wiki") || !slices.Contains(fake.prefs.AdvertiseServices, "svc:foreign") {
		t.Fatal("two-connection reconciliation lost configuration")
	}
}

func TestShouldRejectOtherOrganizationClaimsBeforeCreation(t *testing.T) {
	fake, _, data, config, instance := newFixture(t, "managed")
	other := config.Record
	other.ID, other.OrgID, other.Name = "foreign", "other", "Foreign"
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "other", Name: "other", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateConnection(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	data.endpoints = append(data.endpoints, &store.Endpoint{ID: "foreign-ep", OrgID: "other", Domain: "notes.other.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: other.ID}}}})
	status, err := instance.Sync(context.Background())
	if err == nil || !strings.Contains(status.LastError, "claimed") || len(fake.services) != 0 || fake.servePosts != 0 || fake.prefsPatches != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestShouldPreserveExternallyEditedOwnedResources(t *testing.T) {
	for _, resource := range []string{"api", "api-extra", "serve"} {
		t.Run(resource, func(t *testing.T) {
			fake, _, data, _, instance := newFixture(t, "managed")
			if _, err := instance.Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			if resource == "api-extra" {
				service := fake.services["svc:notes"]
				service.Extra = map[string]json.RawMessage{"future": json.RawMessage(`{"operator":"value"}`)}
				fake.services["svc:notes"] = service
			} else if resource == "api" {
				service := fake.services["svc:notes"]
				service.Comment = "external"
				fake.services["svc:notes"] = service
			} else {
				fake.serve.Services["svc:notes"].TCP[443].HTTPS = false
			}
			data.endpoints[0].Policy.Access = nil
			if _, err := instance.Sync(context.Background()); err == nil {
				t.Fatal("external edit was not protected")
			}
			if fake.services["svc:notes"].Name == "" || fake.serve.Services["svc:notes"] == nil || !slices.Contains(fake.prefs.AdvertiseServices, "svc:notes") {
				t.Fatal("externally edited resources removed")
			}
		})
	}
}

func TestShouldRejectClientOwnedStateAndInvalidConfiguration(t *testing.T) {
	_, provider, _, config, _ := newFixture(t, "imported")
	for _, raw := range []string{`{"tailnet":"example.com","owned_services":{"svc:notes":"ep-notes"}}`, `{"tailnet":"example.com","level":"managed"}`, `{"tailnet":"example.com/foreign"}`, `{"tailnet":"example.com","socket":"relative.sock"}`, `{"tailnet":"example.com","service_tags":["invalid"]}`} {
		input := config
		input.Record.Config = json.RawMessage(raw)
		input.Secrets = nil
		if err := provider.Validate(input); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	registry := connections.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	capability, ok := provider.(connections.AccessHostnameProvider)
	if !ok {
		t.Fatal("missing naming capability")
	}
	name, err := capability.AccessHostname("notes.example.test", "")
	if err != nil || name != "notes" || provider.Describe().Scope != "private" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}

func TestShouldPreserveOwnedServiceWhenAnotherHostOrUnknownMembershipExists(t *testing.T) {
	for _, hosts := range [][]ServiceHost{nil, {{StableNodeID: "foreign-node", ApprovalLevel: "approved:manual", Configured: "ready"}}, {{ApprovalLevel: "approved:auto", Configured: "ready"}}, {{StableNodeID: "node-one", ApprovalLevel: "unknown", Configured: "ready"}}} {
		t.Run(fmt.Sprint(hosts), func(t *testing.T) {
			fake, _, data, config, instance := newFixture(t, "managed")
			if _, err := instance.Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			fake.serviceHosts = map[string][]ServiceHost{"svc:notes": hosts}
			data.endpoints[0].Policy.Access = nil
			before := fake.writes
			status, err := instance.Sync(context.Background())
			if err == nil || status.Health != "degraded" || fake.writes != before || fake.services["svc:notes"].Name == "" || fake.serve.Services["svc:notes"] == nil {
				t.Fatalf("status=%+v err=%v writes=%d/%d", status, err, fake.writes, before)
			}
			record, _ := data.GetConnection(context.Background(), config.Record.ID)
			var state ownershipState
			_ = json.Unmarshal(record.State, &state)
			if state.OwnedServices["svc:notes"] == "" {
				t.Fatal("ownership lost after refused cleanup")
			}
		})
	}
}

func TestShouldProtectOtherOrganizationReferenceDuringOwnedCleanup(t *testing.T) {
	fake, _, data, config, instance := newFixture(t, "managed")
	if _, err := instance.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	other := config.Record
	other.ID, other.OrgID, other.Name = "foreign", "other", "Foreign"
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "other", Name: "other", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateConnection(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	data.endpoints[0].Policy.Access = nil
	data.endpoints = append(data.endpoints, &store.Endpoint{ID: "foreign-ep", OrgID: "other", Domain: "notes.other.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: other.ID}}}})
	before := fake.writes
	status, err := instance.Sync(context.Background())
	if err == nil || !strings.Contains(status.LastError, "claimed") || fake.writes != before || fake.services["svc:notes"].Name == "" || fake.serve.Services["svc:notes"] == nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestShouldProtectForeignAdvertisementDuringPartialCleanup(t *testing.T) {
	fake, _, data, config, instance := newFixture(t, "managed")
	fake.servePostFailure = true
	if _, err := instance.Sync(context.Background()); err == nil {
		t.Fatal("expected partial creation failure")
	}
	fake.servePostFailure = false
	fake.prefs.AdvertiseServices = append(fake.prefs.AdvertiseServices, "svc:notes")
	data.endpoints[0].Policy.Access = nil
	before := fake.writes
	status, err := instance.Sync(context.Background())
	if !slices.Contains(fake.prefs.AdvertiseServices, "svc:notes") {
		t.Fatal("foreign advertisement changed")
	}
	if fake.services["svc:notes"].Name == "" {
		t.Fatalf("deleted service despite observed foreign advertisement; sync error=%v", err)
	}
	if err == nil || status.Health != "degraded" || fake.writes != before {
		t.Fatalf("status=%+v err=%v writes=%d/%d", status, err, fake.writes, before)
	}
	record, _ := data.GetConnection(context.Background(), config.Record.ID)
	var state ownershipState
	_ = json.Unmarshal(record.State, &state)
	if state.OwnedServices["svc:notes"] != "ep-notes" || state.API["svc:notes"] == "" || state.Advertised["svc:notes"] {
		t.Fatalf("ownership changed: %s", record.State)
	}
}

func TestShouldProtectForeignAdvertisementAfterFailedCleanup(t *testing.T) {
	fake, _, data, config, instance := newFixture(t, "managed")
	fake.servePostFailure = true
	if _, err := instance.Sync(context.Background()); err == nil {
		t.Fatal("expected partial creation failure")
	}
	fake.servePostFailure = false
	data.endpoints[0].Policy.Access = nil
	fake.serviceHosts = map[string][]ServiceHost{"svc:notes": nil}
	if _, err := instance.Sync(context.Background()); err == nil {
		t.Fatal("expected unknown membership cleanup failure")
	}
	fake.serviceHosts = nil
	fake.prefs.AdvertiseServices = append(fake.prefs.AdvertiseServices, "svc:notes")
	before := fake.writes
	status, err := instance.Sync(context.Background())
	if err == nil || status.Health != "degraded" || fake.writes != before || fake.services["svc:notes"].Name == "" || !slices.Contains(fake.prefs.AdvertiseServices, "svc:notes") {
		t.Fatalf("foreign advertisement did not protect retry: status=%+v err=%v", status, err)
	}
	record, _ := data.GetConnection(context.Background(), config.Record.ID)
	var state ownershipState
	_ = json.Unmarshal(record.State, &state)
	if state.OwnedServices["svc:notes"] != "ep-notes" || state.API["svc:notes"] == "" {
		t.Fatalf("ownership lost: %s", record.State)
	}
}

func TestShouldRereadForeignAdvertisementBeforeEveryCleanupStage(t *testing.T) {
	for _, read := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			fake, _, data, config, instance := newFixture(t, "managed")
			fake.servePostFailure = true
			if _, err := instance.Sync(context.Background()); err == nil {
				t.Fatal("expected partial creation failure")
			}
			fake.servePostFailure = false
			data.endpoints[0].Policy.Access = nil
			fake.prefsReads, fake.advertiseAtPrefsRead = 0, read
			before := fake.writes
			status, err := instance.Sync(context.Background())
			if err == nil || status.Health != "degraded" || fake.writes != before || fake.services["svc:notes"].Name == "" || !slices.Contains(fake.prefs.AdvertiseServices, "svc:notes") {
				t.Fatalf("cleanup ignored observed foreign claim: status=%+v err=%v reads=%d", status, err, fake.prefsReads)
			}
			record, _ := data.GetConnection(context.Background(), config.Record.ID)
			var state ownershipState
			_ = json.Unmarshal(record.State, &state)
			if state.OwnedServices["svc:notes"] != "ep-notes" || state.API["svc:notes"] == "" {
				t.Fatalf("ownership lost: %s", record.State)
			}
		})
	}
}

func TestShouldRefuseLocalCleanupWhenForeignAdvertisementClaimsOwnedServeEntry(t *testing.T) {
	fake, _, data, config, instance := newFixture(t, "managed")
	fake.prefsPatchFailure = true
	if _, err := instance.Sync(context.Background()); err == nil {
		t.Fatal("expected advertisement creation failure")
	}
	fake.prefsPatchFailure = false
	serve := fake.serve.Clone()
	fake.prefs.AdvertiseServices = append(fake.prefs.AdvertiseServices, "svc:notes")
	data.endpoints[0].Policy.Access = nil
	before := fake.writes
	status, err := instance.Sync(context.Background())
	if err == nil || status.Health != "degraded" || fake.writes != before || !reflect.DeepEqual(fake.serve, serve) || fake.services["svc:notes"].Name == "" || !slices.Contains(fake.prefs.AdvertiseServices, "svc:notes") {
		t.Fatalf("local cleanup disrupted foreign claim: status=%+v err=%v", status, err)
	}
	record, _ := data.GetConnection(context.Background(), config.Record.ID)
	var state ownershipState
	_ = json.Unmarshal(record.State, &state)
	if state.API["svc:notes"] == "" || state.Serve["svc:notes"] == "" || state.Advertised["svc:notes"] {
		t.Fatalf("ownership changed: %s", record.State)
	}
}

func TestShouldRejectPrerequisitesAndRedactFailures(t *testing.T) {
	for _, mode := range []string{"socket-down", "old-version", "untagged", "oauth", "api", "tailnet", "api-node", "foreign-api", "foreign-serve", "foreign-advertisement", "api-create-failed", "api-read-edit"} {
		t.Run(mode, func(t *testing.T) {
			fake, _, data, config, instance := newFixture(t, "managed")
			switch mode {
			case "socket-down":
				fake.localFailure = true
			case "old-version":
				fake.status.Version = "1.84.0"
			case "untagged":
				fake.status.Self.Tags = tags()
			case "oauth":
				fake.oauthFailure = true
			case "api":
				fake.apiFailure = true
			case "tailnet":
				fake.status.CurrentTailnet.Name = "foreign.com"
			case "api-node":
				fake.nodeMismatch = true
			case "foreign-api":
				fake.services["svc:notes"] = Service{Name: "svc:notes", Ports: []string{"tcp:443"}}
			case "foreign-serve":
				fake.serve.Services["svc:notes"] = serviceConfig("svc:notes", "test.ts.net", "http://127.0.0.1:8090")
			case "foreign-advertisement":
				fake.prefs.AdvertiseServices = append(fake.prefs.AdvertiseServices, "svc:notes")
			case "api-create-failed":
				fake.apiPutFailure = true
			case "api-read-edit":
				fake.apiReadEdit = true
			}
			status, err := instance.Sync(context.Background())
			if err == nil || status.Health != "degraded" || strings.Contains(status.LastError, "secret") {
				t.Fatalf("status=%+v err=%v", status, err)
			}
			record, _ := data.GetConnection(context.Background(), config.Record.ID)
			if len(record.State) > 0 {
				t.Fatalf("failed creation acquired ownership: %s", record.State)
			}
			if fake.servePosts != 0 || fake.prefsPatches != 0 {
				t.Fatal("prerequisite failure wrote LocalAPI")
			}
		})
	}
}

func TestShouldRereadETagAndBoundRetries(t *testing.T) {
	for _, conflicts := range []int{1, 5} {
		t.Run(fmt.Sprint(conflicts), func(t *testing.T) {
			fake, _, _, _, instance := newFixture(t, "managed")
			fake.conflicts = conflicts
			status, err := instance.Sync(context.Background())
			if conflicts == 1 {
				if err != nil || fake.servePosts != 2 || fake.serve.Services["svc:late-foreign"] == nil {
					t.Fatalf("status=%+v err=%v posts=%d", status, err, fake.servePosts)
				}
			} else if err == nil || fake.servePosts != 3 || status.Health != "degraded" {
				t.Fatalf("status=%+v err=%v posts=%d", status, err, fake.servePosts)
			}
		})
	}
}

func TestShouldKeepSuccessfulAPIOwnershipAfterServeFailure(t *testing.T) {
	fake, _, data, config, instance := newFixture(t, "managed")
	fake.servePostFailure = true
	if _, err := instance.Sync(context.Background()); err == nil {
		t.Fatal("Serve failure accepted")
	}
	record, _ := data.GetConnection(context.Background(), config.Record.ID)
	var state ownershipState
	_ = json.Unmarshal(record.State, &state)
	if state.OwnedServices["svc:notes"] != "ep-notes" || state.API["svc:notes"] == "" || state.Serve["svc:notes"] != "" || state.Advertised["svc:notes"] {
		t.Fatalf("state=%s", record.State)
	}
	fake.servePostFailure = false
	if _, err := instance.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShouldNotPublishUnadvertisedOrUnapprovedServices(t *testing.T) {
	for _, advertisement := range []bool{false, true} {
		t.Run(fmt.Sprint(advertisement), func(t *testing.T) {
			fake, _, _, _, instance := newFixture(t, "imported")
			fake.serve.Services["svc:notes"] = serviceConfig("svc:notes", "test.ts.net", "http://127.0.0.1:8090")
			if advertisement {
				fake.prefs.AdvertiseServices = append(fake.prefs.AdvertiseServices, "svc:notes")
			} else {
				fake.status.Self.CapMap[tailcfg.NodeAttrServiceHost] = []tailcfg.RawMessage{`{"svc:notes":["100.100.100.42"]}`}
			}
			status, err := instance.Sync(context.Background())
			if err != nil || status.Hostnames[0].State != "pending" {
				t.Fatalf("status=%+v err=%v", status, err)
			}
		})
	}
}
