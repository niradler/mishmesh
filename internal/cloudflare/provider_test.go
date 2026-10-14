package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type providerData struct {
	store.DataStore
	endpointList
}

func (d providerData) ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error) {
	return d.endpointList.ListEndpointsByOrg(ctx, orgID)
}

func (d providerData) ListOrgs(context.Context) ([]*store.Org, error) {
	orgs := make(map[string]bool)
	for _, endpoint := range d.endpoints {
		if endpoint != nil {
			orgs[endpoint.OrgID] = true
		}
	}
	result := make([]*store.Org, 0, len(orgs))
	for id := range orgs {
		result = append(result, &store.Org{ID: id})
	}
	return result, nil
}

func (d providerData) UpdateConnectionState(context.Context, string, string, json.RawMessage) error {
	return nil
}

func providerConfig(t *testing.T, id, metrics string, managed bool) connections.Config {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"account_id": testAccount, "tunnel_id": testTunnel, "access_app_id": testAppID, "metrics_addr": metrics, "connector": ConnectorExternal, "prune": managed})
	if err != nil {
		t.Fatal(err)
	}
	secret := map[string]string{TokenKindTunnel: encodeToken(t, `{"a":"acct1","t":"tun1","s":"private"}`)}
	if managed {
		secret[TokenKindAPI] = testToken
	}
	return connections.Config{Record: store.Connection{ID: id, OrgID: "org", Type: "cloudflare", Name: id, Config: raw}, Secrets: secret}
}

func genericHost(t *testing.T, status connections.Status, host string) connections.HostStatus {
	t.Helper()
	for _, entry := range status.Hostnames {
		if entry.Host == host {
			return entry
		}
	}
	t.Fatalf("host %s absent: %+v", host, status)
	return connections.HostStatus{}
}

func TestShouldOpenIndependentCloudflareConnections(t *testing.T) {
	data := providerData{endpointList: endpointList{[]*store.Endpoint{
		{ID: "a", OrgID: "org", Domain: "a.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}},
		{ID: "b", OrgID: "org", Domain: "b.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "b"}}}},
		{ID: "off", OrgID: "org", Domain: "off.example.com"},
		{ID: "other", OrgID: "other", Domain: "other.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}},
	}}}
	fakes := []*fakeCF{newFakeCF(t), newFakeCF(t)}
	for index, id := range []string{"a", "b"} {
		fake := fakes[index]
		fake.local.setIngress(`[{"hostname":"a.example.com","service":"http://127.0.0.1:8080"},{"hostname":"b.example.com","service":"http://127.0.0.1:8080"},{"hostname":"off.example.com","service":"http://127.0.0.1:9000"},{"service":"http_status:404"}]`)
		provider := NewProvider(ProviderOptions{BaseURL: fake.server.URL})
		instance, err := provider.Open(providerConfig(t, id, fake.local.addr(), false), connections.Deps{Data: data, Origin: testOrigin})
		if err != nil {
			t.Fatal(err)
		}
		defer instance.Close()
		status, err := instance.Sync(context.Background())
		if err != nil || status.Level != LevelImported || status.Health != TunnelHealthy {
			t.Fatalf("instance %s: %+v %v", id, status, err)
		}
		if host := genericHost(t, status, id+".example.com"); host.State != StatePublished || host.EndpointID != id {
			t.Fatalf("selected=%+v", host)
		}
		foreign := []string{"b", "a"}[index]
		if host := genericHost(t, status, foreign+".example.com"); host.State != StateUnclaimed || host.EndpointID != foreign || host.Fix == "" {
			t.Fatalf("foreign=%+v", host)
		}
		if host := genericHost(t, status, "off.example.com"); host.State != StateUnclaimed || host.EndpointID != "off" {
			t.Fatalf("observer=%+v", host)
		}
		if fake.configPuts != 0 || fake.dnsPosts != 0 || fake.appPuts != 0 || tunnelGets(fake) != 0 {
			t.Fatal("imported connection wrote upstream")
		}
	}
}

func TestShouldPreserveProviderPendingAndConflictFixes(t *testing.T) {
	fake := newFakeCF(t)
	fake.local.setIngress(`[{"hostname":"conflict.example.com","service":"http://127.0.0.1:9000"},{"service":"http_status:404"}]`)
	data := providerData{endpointList: endpointList{[]*store.Endpoint{
		{ID: "pending", OrgID: "org", Domain: "pending.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}},
		{ID: "conflict", OrgID: "org", Domain: "conflict.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}},
	}}}
	instance, err := NewProvider(ProviderOptions{}).Open(providerConfig(t, "a", fake.local.addr(), false), connections.Deps{Data: data, Origin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	status, err := instance.Sync(context.Background())
	if err != nil || status.Health != "degraded" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	for _, test := range []struct{ host, state, action string }{{"pending.example.com", StatePending, "add"}, {"conflict.example.com", StateConflict, "edit"}} {
		if host := genericHost(t, status, test.host); host.State != test.state || !strings.Contains(host.Fix, test.action) || !strings.Contains(host.Fix, testOrigin) {
			t.Fatalf("host=%+v", host)
		}
	}
}

func TestShouldValidateCloudflareSecretSeparation(t *testing.T) {
	provider := NewProvider(ProviderOptions{})
	registry := connections.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	descriptor := provider.Describe()
	if descriptor.Type != "cloudflare" || descriptor.Role != "access" || descriptor.Scope != "public" || !descriptor.Detect {
		t.Fatalf("descriptor=%+v", descriptor)
	}
	valid := providerConfig(t, "a", "127.0.0.1:20241", false)
	if err := provider.Validate(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"tunnel_token":"private"}`, `{"api_token":"private"}`, `{"metrics_addr":"10.0.0.1:20241"}`, `{"connector":"bad"}`, `{"origin":"http://user:secret@localhost"}`, `{"prune":true}`} {
		cfg := valid
		cfg.Record.Config = json.RawMessage(raw)
		if err := provider.Validate(cfg); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	valid.Secrets = map[string]string{TokenKindTunnel: "not-a-token"}
	if err := provider.Validate(valid); err == nil {
		t.Fatal("accepted invalid tunnel token")
	}
}

func TestShouldDetectThroughCloudflareProvider(t *testing.T) {
	fake := newFakeCF(t)
	fake.local.setIngress(`[{"hostname":"a.example.com","service":"http://127.0.0.1:8080"},{"service":"http_status:404"}]`)
	input, _ := json.Marshal(map[string]string{"metrics_addr": fake.local.addr(), "tunnel_token": encodeToken(t, `{"a":"acct1","t":"tun1","s":"private"}`)})
	raw, err := NewProvider(ProviderOptions{}).Detect(context.Background(), input)
	if err != nil || bytes.Contains(raw, []byte("private")) || bytes.Contains(raw, []byte("tunnel_token")) {
		t.Fatalf("detect=%s err=%v", raw, err)
	}
	var result DetectResult
	if err := json.Unmarshal(raw, &result); err != nil || result.AccountID != testAccount || result.TunnelID != testTunnel || len(result.Hostnames) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type failingTransport struct{ message string }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(f.message)
}

func TestShouldSanitizeProviderErrorsAndLogs(t *testing.T) {
	fake := newFakeCF(t)
	var logs bytes.Buffer
	provider := NewProvider(ProviderOptions{NewClient: func(base, token string) *Client {
		client := NewClient(base, token)
		client.http.Transport = failingTransport{"unexpected remote-private response"}
		client.backoff = 0
		return client
	}})
	instance, err := provider.Open(providerConfig(t, "a", fake.local.addr(), true), connections.Deps{Data: providerData{}, Origin: testOrigin, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	status, err := instance.Sync(context.Background())
	if err == nil || status.LastError == "" || status.Health != "degraded" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw)+err.Error()+logs.String(), "remote-private") {
		t.Fatalf("remote response leaked: %s %v %s", raw, err, &logs)
	}
}

func TestShouldOnlyManageSelectedConnectionRoutes(t *testing.T) {
	fake := newFakeCF(t)
	fake.setConfig(t, `{"ingress":[{"hostname":"b.example.com","service":"http://127.0.0.1:8080"},{"hostname":"orphan.example.com","service":"http://127.0.0.1:8080"},{"service":"http_status:404"}]}`)
	fake.setApp(t, `{"name":"shared","self_hosted_domains":["b.example.com","orphan.example.com"]}`)
	fake.records["z-example"] = []DNSRecord{{ID: "foreign", Type: "CNAME", Name: "b.example.com", Content: testTunnel + ".cfargotunnel.com"}, {ID: "orphan", Type: "CNAME", Name: "orphan.example.com", Content: testTunnel + ".cfargotunnel.com"}}
	data := providerData{endpointList: endpointList{[]*store.Endpoint{
		{ID: "a", OrgID: "org", Domain: "a.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}},
		{ID: "b", OrgID: "org", Domain: "b.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "b"}}}},
	}}}
	instance, err := NewProvider(ProviderOptions{BaseURL: fake.server.URL}).Open(providerConfig(t, "a", fake.local.addr(), true), connections.Deps{Data: data, Origin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	status, err := instance.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if host := genericHost(t, status, "a.example.com"); host.State != StatePublished {
		t.Fatalf("selected=%+v", host)
	}
	for _, host := range []string{"b.example.com", "orphan.example.com"} {
		if entry := genericHost(t, status, host); entry.State != StateUnclaimed {
			t.Fatalf("unclaimed=%+v", entry)
		}
	}
	if fake.dnsDeletes != 0 || !strings.Contains(strings.Join(fake.ingressServices(), ","), "orphan.example.com|"+testOrigin) || !accessHasHost(AccessApp(fake.app), "orphan.example.com") {
		t.Fatalf("unclaimed ownership changed: deletes=%d ingress=%v app=%v", fake.dnsDeletes, fake.ingressServices(), fake.app)
	}
	if fake.dnsPosts != 1 || fake.appPuts != 1 || !accessHasHost(AccessApp(fake.app), "a.example.com") || !accessHasHost(AccessApp(fake.app), "b.example.com") {
		t.Fatalf("selected reconciliation=%+v", status)
	}
	puts, posts, appPuts := fake.configPuts, fake.dnsPosts, fake.appPuts
	if _, err := instance.Sync(context.Background()); err != nil || fake.configPuts != puts || fake.dnsPosts != posts || fake.appPuts != appPuts {
		t.Fatalf("repeated sync changed routes: err=%v", err)
	}
}

func TestShouldOwnAndStopIndependentManagedConnectors(t *testing.T) {
	t.Setenv(helperEnv, "1")
	t.Setenv(helperExitEnv, "1")
	instances := make([]connections.Instance, 0, 2)
	for _, id := range []string{"a", "b"} {
		config := providerConfig(t, id, freeAddr(t), false)
		var values map[string]any
		if err := json.Unmarshal(config.Record.Config, &values); err != nil {
			t.Fatal(err)
		}
		values["connector"] = ConnectorManaged
		config.Record.Config, _ = json.Marshal(values)
		instance, err := NewProvider(ProviderOptions{CloudflaredBin: os.Args[0], NewSupervisor: func(cfg SupervisorConfig, log *slog.Logger) *Supervisor {
			supervisor := NewSupervisor(cfg, log)
			supervisor.minBackoff, supervisor.maxBackoff = 5*time.Millisecond, 20*time.Millisecond
			return supervisor
		}}).Open(config, connections.Deps{Data: providerData{}, Origin: testOrigin, Log: quietProviderLog})
		if err != nil {
			t.Fatal(err)
		}
		instances = append(instances, instance)
		t.Cleanup(instance.Close)
		go instance.Run(context.Background())
	}
	for _, instance := range instances {
		waitFor(t, func() bool {
			status, ok := instance.Status().Details["supervisor"].(SupervisorStatus)
			return ok && status.Restarts >= 2
		})
	}
	instances[0].Close()
	first := instances[0].Status().Details["supervisor"].(SupervisorStatus)
	second := instances[1].Status().Details["supervisor"].(SupervisorStatus)
	waitFor(t, func() bool {
		return instances[1].Status().Details["supervisor"].(SupervisorStatus).Restarts > second.Restarts
	})
	if current := instances[0].Status().Details["supervisor"].(SupervisorStatus); current.Restarts != first.Restarts || current.Running {
		t.Fatalf("closed supervisor restarted: before=%+v after=%+v", first, current)
	}
	instances[1].Close()
}

type ownershipData struct {
	store.DataStore
	endpoints []*store.Endpoint
}

func (d *ownershipData) ListEndpointsByOrg(_ context.Context, orgID string) ([]*store.Endpoint, error) {
	var result []*store.Endpoint
	for _, endpoint := range d.endpoints {
		if endpoint.OrgID == orgID {
			result = append(result, endpoint)
		}
	}
	return result, nil
}

type ownedFixture struct {
	fake   *fakeCF
	data   *ownershipData
	path   string
	config connections.Config
	open   connections.Provider
}

func newOwnedFixture(t *testing.T) *ownedFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ownership.db")
	data, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	for _, id := range []string{"org", "foreign"} {
		if err := data.CreateOrg(context.Background(), &store.Org{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	fake := newFakeCF(t)
	fake.setConfig(t, `{"ingress":[{"hostname":"orphan.example.com","service":"http://127.0.0.1:8080"},{"service":"http_status:404"}]}`)
	fake.setApp(t, `{"self_hosted_domains":["orphan.example.com"],"destinations":[{"type":"public","uri":"orphan.example.com"}]}`)
	config := providerConfig(t, "a", fake.local.addr(), true)
	if err := data.CreateConnection(context.Background(), &config.Record); err != nil {
		t.Fatal(err)
	}
	return &ownedFixture{fake: fake, path: path, config: config, open: NewProvider(ProviderOptions{BaseURL: fake.server.URL}), data: &ownershipData{DataStore: data,
		endpoints: []*store.Endpoint{{ID: "ep", OrgID: "org", Domain: "a.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}}}}}
}

func (f *ownedFixture) sync(t *testing.T) connections.Status {
	t.Helper()
	record, err := f.data.GetConnection(context.Background(), f.config.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := f.config
	config.Record = *record
	instance, err := f.open.Open(config, connections.Deps{Data: f.data, Origin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	status, err := instance.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestShouldPersistOwnedRoutePruningAcrossStoreReopen(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.sync(t)
	record, err := fixture.data.GetConnection(context.Background(), "a")
	if err != nil || len(record.State) == 0 {
		t.Fatalf("ownership not persisted: %+v %v", record, err)
	}
	fixture.data.DataStore.(*sqlite.Store).Close()
	reopened, err := sqlite.Open(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fixture.data.DataStore = reopened
	fixture.data.endpoints[0].Policy.Access = nil
	fixture.sync(t)
	if fixture.fake.dnsDeletes != 1 || strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "a.example.com|") || accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
		t.Fatalf("owned route not pruned: ingress=%v deletes=%d app=%v", fixture.fake.ingressServices(), fixture.fake.dnsDeletes, fixture.fake.app)
	}
	if !strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "orphan.example.com|") || !accessHasHost(AccessApp(fixture.fake.app), "orphan.example.com") {
		t.Fatal("unclaimed orphan was modified")
	}
}

func TestShouldProtectReclaimedAndModifiedOwnedResources(t *testing.T) {
	for _, resource := range []string{"foreign-claim", "ingress", "dns", "access", "selected-ingress", "selected-dns", "selected-dns-ttl", "selected-access"} {
		t.Run(resource, func(t *testing.T) {
			fixture := newOwnedFixture(t)
			fixture.sync(t)
			kind := strings.TrimPrefix(resource, "selected-")
			switch kind {
			case "foreign-claim":
				fixture.data.endpoints = append(fixture.data.endpoints, &store.Endpoint{ID: "foreign", OrgID: "foreign", Domain: "a.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "other"}}}})
			case "ingress":
				fixture.fake.mu.Lock()
				for _, rule := range fixture.fake.config["ingress"].([]any) {
					if object := rule.(map[string]any); object["hostname"] == "a.example.com" {
						object["originRequest"] = map[string]any{"connectTimeout": 99}
					}
				}
				fixture.fake.mu.Unlock()
			case "dns":
				fixture.fake.records["z-example"][0].ID = "replacement-record"
			case "dns-ttl":
				fixture.fake.records["z-example"][0].TTL = 300
			case "access":
				for _, entry := range fixture.fake.app["destinations"].([]any) {
					if object := entry.(map[string]any); object["uri"] == "a.example.com" {
						object["port"] = 8443
					}
				}
			}
			if strings.HasPrefix(resource, "selected-") {
				fixture.sync(t)
			}
			fixture.data.endpoints[0].Policy.Access = nil
			fixture.sync(t)
			if kind == "foreign-claim" || kind == "ingress" {
				if fixture.fake.dnsDeletes != 0 || !strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "a.example.com|") || !accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
					t.Fatal("reclaimed or modified ingress ownership was removed")
				}
			} else if strings.HasPrefix(kind, "dns") && fixture.fake.dnsDeletes != 0 {
				t.Fatal("modified DNS resource removed")
			} else if kind == "access" && !accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
				t.Fatal("modified Access resource removed")
			}
		})
	}
}

func TestShouldRejectMalformedPrivateCloudflareState(t *testing.T) {
	fake := newFakeCF(t)
	for _, raw := range []string{`null`, `{"version":2,"routes":{}}`, `{"version":1,"routes":{"a.example.com":{"ingress":"private-invalid"}}}`, `{"version":1,"routes":{},"private-invalid":true}`} {
		config := providerConfig(t, "a", fake.local.addr(), true)
		config.Record.State = json.RawMessage(raw)
		_, err := NewProvider(ProviderOptions{}).Open(config, connections.Deps{Data: providerData{}, Origin: testOrigin, Log: quietProviderLog})
		if err == nil || strings.Contains(err.Error(), "private-invalid") {
			t.Fatalf("invalid private state: %s err=%v", raw, err)
		}
	}
}

func TestShouldRetryOwnedResourcePruningAfterIngressAlreadyDisappeared(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.sync(t)
	fixture.data.endpoints[0].Policy.Access = nil
	fixture.fake.mu.Lock()
	rules := fixture.fake.config["ingress"].([]any)
	kept := make([]any, 0, len(rules))
	for _, rule := range rules {
		if rule.(map[string]any)["hostname"] != "a.example.com" {
			kept = append(kept, rule)
		}
	}
	fixture.fake.config["ingress"] = kept
	fixture.fake.mu.Unlock()
	fixture.sync(t)
	if fixture.fake.dnsDeletes != 1 || accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
		t.Fatalf("owned residual resources retained: deletes=%d app=%v", fixture.fake.dnsDeletes, fixture.fake.app)
	}
}

func TestShouldRecordNormalizedRemoteOwnershipAndIgnoreOtherHostEdits(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.fake.normalizeIngress, fixture.fake.normalizeAccess = true, true
	fixture.sync(t)
	fixture.data.endpoints[0].Policy.Access = nil
	fixture.fake.mu.Lock()
	for _, destination := range fixture.fake.app["destinations"].([]any) {
		if object := destination.(map[string]any); object["uri"] == "orphan.example.com" {
			object["port"] = 8443
		}
	}
	fixture.fake.mu.Unlock()
	fixture.sync(t)
	if fixture.fake.dnsDeletes != 1 || strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "a.example.com|") || accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
		t.Fatalf("normalized owned resource not pruned: deletes=%d ingress=%v app=%v", fixture.fake.dnsDeletes, fixture.fake.ingressServices(), fixture.fake.app)
	}
	if !accessHasHost(AccessApp(fixture.fake.app), "orphan.example.com") {
		t.Fatal("foreign-host edit was overwritten")
	}
	for _, destination := range fixture.fake.app["destinations"].([]any) {
		if object := destination.(map[string]any); object["uri"] == "orphan.example.com" && object["port"] != float64(8443) {
			t.Fatalf("foreign destination edit lost: %v", object)
		}
	}
}

func TestShouldSerializeSharedRemoteResourcesWithoutBlockingOtherTunnels(t *testing.T) {
	fake := newFakeCF(t)
	fake.setConfig(t, `{"ingress":[{"service":"http_status:404"}]}`)
	fake.setApp(t, `{"self_hosted_domains":[]}`)
	data := providerData{endpointList: endpointList{[]*store.Endpoint{
		{ID: "a", OrgID: "org", Domain: "a.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}},
		{ID: "b", OrgID: "org", Domain: "b.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "b"}}}},
	}}}
	provider := NewProvider(ProviderOptions{BaseURL: fake.server.URL})
	instances := make([]connections.Instance, 0, 2)
	for _, id := range []string{"a", "b"} {
		instance, err := provider.Open(providerConfig(t, id, fake.local.addr(), true), connections.Deps{Data: data, Origin: testOrigin})
		if err != nil {
			t.Fatal(err)
		}
		defer instance.Close()
		instances = append(instances, instance)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, instance := range instances {
		go func() {
			<-start
			_, err := instance.Sync(context.Background())
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"a.example.com", "b.example.com"} {
		if !strings.Contains(strings.Join(fake.ingressServices(), ","), host+"|") || !accessHasHost(AccessApp(fake.app), host) {
			t.Fatalf("concurrent route lost: ingress=%v app=%v", fake.ingressServices(), fake.app)
		}
	}
	locks := providerLocks{}
	held, err := locks.acquire(context.Background(), &Config{AccountID: "account", TunnelID: "first", AccessAppID: "app"})
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := locks.acquire(ctx, &Config{AccountID: "account", TunnelID: "second", AccessAppID: "app"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared Access lock did not honor cancellation: %v", err)
	}
	independent, err := locks.acquire(context.Background(), &Config{AccountID: "account", TunnelID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	independent()
}

func TestShouldReloadOnlyOneManagedCloudflareHubInstance(t *testing.T) {
	t.Setenv(helperEnv, "1")
	t.Setenv(helperExitEnv, "1")
	fixture := newHubFixture(t)
	var supervisorMu sync.Mutex
	supervisors := make(map[string]*Supervisor)
	provider := NewProvider(ProviderOptions{CloudflaredBin: os.Args[0], NewSupervisor: func(config SupervisorConfig, log *slog.Logger) *Supervisor {
		supervisor := NewSupervisor(config, log)
		supervisor.minBackoff, supervisor.maxBackoff = 5*time.Millisecond, 20*time.Millisecond
		supervisorMu.Lock()
		supervisors[config.MetricsAddr] = supervisor
		supervisorMu.Unlock()
		return supervisor
	}})
	registry := connections.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	addresses := []string{freeAddr(t), freeAddr(t)}
	for index, id := range []string{"a", "b"} {
		config := providerConfig(t, id, addresses[index], false)
		config.Record.OrgID = "org_a"
		var values map[string]any
		_ = json.Unmarshal(config.Record.Config, &values)
		values["connector"] = ConnectorManaged
		config.Record.Config, _ = json.Marshal(values)
		plain, _ := json.Marshal(config.Secrets)
		sealed, err := fixture.box.Seal(plain, connections.SecretContext("org_a", id))
		if err != nil {
			t.Fatal(err)
		}
		config.Record.SecretsEnc = sealed
		if err := fixture.data.CreateConnection(context.Background(), &config.Record); err != nil {
			t.Fatal(err)
		}
	}
	hub := connections.NewHub(connections.HubOptions{Registry: registry, Deps: connections.Deps{Data: fixture.data, Box: fixture.box, Origin: testOrigin, Log: quietProviderLog}})
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	for _, id := range []string{"a", "b"} {
		waitFor(t, func() bool {
			raw, err := json.Marshal(hub.Status(id).Details["supervisor"])
			var status SupervisorStatus
			return err == nil && json.Unmarshal(raw, &status) == nil && status.Restarts >= 2
		})
	}
	supervisorMu.Lock()
	old, other := supervisors[addresses[0]], supervisors[addresses[1]]
	supervisorMu.Unlock()
	if err := hub.Reload(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	beforeOld, beforeOther := old.Status().Restarts, other.Status().Restarts
	waitFor(t, func() bool { return other.Status().Restarts > beforeOther })
	if status := old.Status(); status.Running || status.Restarts != beforeOld {
		t.Fatalf("retired supervisor running after reload: %+v", status)
	}
	supervisorMu.Lock()
	replacement := supervisors[addresses[0]]
	supervisorMu.Unlock()
	if replacement == old {
		t.Fatal("reload did not replace the selected instance")
	}
	hub.Close()
	if replacement.Status().Running || other.Status().Running {
		t.Fatal("hub close left a managed connector running")
	}
}

type failingDNSTransport struct{ base http.RoundTripper }

func (f failingDNSTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.Contains(request.URL.Path, "dns_records") {
		return nil, errors.New("transformed truncated remote-private output")
	}
	return f.base.RoundTrip(request)
}

func TestShouldKeepPublisherDiagnosticsStableAndSanitized(t *testing.T) {
	fake := newFakeCF(t)
	fake.setConfig(t, `{"ingress":[{"service":"http_status:404"}]}`)
	fake.setApp(t, `{"self_hosted_domains":[]}`)
	var logs bytes.Buffer
	provider := NewProvider(ProviderOptions{BaseURL: fake.server.URL, NewClient: func(base, token string) *Client {
		client := NewClient(base, token)
		client.http.Transport = failingDNSTransport{base: http.DefaultTransport}
		client.backoff = 0
		return client
	}})
	data := providerData{endpointList: endpointList{[]*store.Endpoint{{ID: "a", OrgID: "org", Domain: "a.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "a"}}}}}}}
	instance, err := provider.Open(providerConfig(t, "a", fake.local.addr(), true), connections.Deps{Data: data, Origin: testOrigin, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	instance.(*providerInstance).publisher.syncLogged(context.Background())
	status := instance.Status()
	raw, _ := json.Marshal(status)
	if genericHost(t, status, "a.example.com").State != StateError || !strings.Contains(logs.String(), "cloudflare hostname error") || strings.Contains(string(raw)+logs.String(), "remote-private") {
		t.Fatalf("diagnostics=%s logs=%s", raw, &logs)
	}
}

var quietProviderLog = slog.New(slog.NewTextHandler(io.Discard, nil))
