package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeServiceControl struct {
	mu       sync.Mutex
	statuses map[string]services.Status
	applied  []services.Definition
	ensured  []string
	stopped  []string
	startErr error
}

func newFakeServices() *fakeServiceControl {
	return &fakeServiceControl{statuses: map[string]services.Status{}}
}

func (f *fakeServiceControl) Apply(defs []services.Definition) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = defs
	next := make(map[string]services.Status, len(defs))
	for _, d := range defs {
		st, ok := f.statuses[d.ID]
		if !ok {
			st = services.Status{State: services.StateSleeping}
		}
		st.ID, st.Name, st.Driver = d.ID, d.Name, d.Driver
		next[d.ID] = st
	}
	f.statuses = next
}

func (f *fakeServiceControl) Status(id string) (services.Status, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.statuses[id]
	return st, ok
}

func (f *fakeServiceControl) EnsureRunning(_ context.Context, id, ready string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, id+"@"+ready)
	if f.startErr != nil {
		return f.startErr
	}
	st := f.statuses[id]
	st.State = services.StateRunning
	f.statuses[id] = st
	return nil
}

func (f *fakeServiceControl) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	st := f.statuses[id]
	st.State = services.StateSleeping
	f.statuses[id] = st
	return nil
}

func (f *fakeServiceControl) appliedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.applied))
	for _, d := range f.applied {
		out = append(out, d.Name)
	}
	return out
}

type fakeCloudflare struct {
	mu       sync.Mutex
	statuses map[string]cloudflare.Status
	triggers map[string]int
	reloads  map[string]int
	syncs    map[string]int
	syncErr  error
}

func newFakeCloudflare() *fakeCloudflare {
	return &fakeCloudflare{statuses: map[string]cloudflare.Status{}, triggers: map[string]int{}, reloads: map[string]int{}, syncs: map[string]int{}}
}

func (f *fakeCloudflare) DefaultOrigin() string { return "http://127.0.0.1:8090" }

func (f *fakeCloudflare) Status(orgID string) cloudflare.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.statuses[orgID]; ok {
		return st
	}
	return emptyCloudflareStatus()
}

func (f *fakeCloudflare) setStatus(orgID string, st cloudflare.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[orgID] = st
}

func (f *fakeCloudflare) Reload(_ context.Context, orgID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads[orgID]++
	return nil
}

func (f *fakeCloudflare) Sync(_ context.Context, orgID string) (cloudflare.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs[orgID]++
	st, ok := f.statuses[orgID]
	if !ok {
		return emptyCloudflareStatus(), cloudflare.ErrNotConnected
	}
	if f.syncErr != nil {
		st.LastError = f.syncErr.Error()
	}
	return st, f.syncErr
}

func (f *fakeCloudflare) Trigger(orgID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggers[orgID]++
}

func (f *fakeCloudflare) triggerCount(orgID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers[orgID]
}

func (f *fakeCloudflare) syncCount(orgID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncs[orgID]
}

func (f *fakeCloudflare) reloadCount(orgID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads[orgID]
}

func testSecretBox(t *testing.T) *secrets.Box {
	t.Helper()
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func testTunnelToken(account, tunnel string) string {
	return base64.StdEncoding.EncodeToString([]byte(`{"a":"` + account + `","t":"` + tunnel + `","s":"c2VjcmV0"}`))
}

type fakeHealth struct {
	mu      sync.Mutex
	results map[string]health.EndpointHealth
	probed  []string
	forgot  []string
}

func (f *fakeHealth) Get(id string) (health.EndpointHealth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.results[id]
	return h, ok
}

func (f *fakeHealth) ProbeNow(_ context.Context, id string) (health.EndpointHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probed = append(f.probed, id)
	return f.results[id], nil
}

func (f *fakeHealth) Forget(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, id)
}

type networkFixture struct {
	api *API
	srv *httptest.Server
	svc *fakeServiceControl
	cf  *fakeCloudflare
}

func newNetworkTestAPI(t *testing.T) (*API, *httptest.Server) {
	t.Helper()
	api, srv := newTestAPI(t)
	_ = api.data.CreateOrg(context.Background(), &store.Org{ID: defaultOrgID, Name: "default", CreatedAt: time.Now()})
	proxy.Register(context.Background(), api.data, api.conns, nil, nil)
	return api, srv
}

func newNetworkFixture(t *testing.T) *networkFixture {
	t.Helper()
	api, srv := newNetworkTestAPI(t)
	f := &networkFixture{api: api, srv: srv, svc: newFakeServices(), cf: newFakeCloudflare()}
	api.SetServices(f.svc)
	api.SetCloudflare(f.cf, testSecretBox(t))
	api.SetHostCommands(true)
	api.detectCloudflare = func(_ context.Context, addr, token string) (cloudflare.DetectResult, error) {
		if token == "bogus" {
			return cloudflare.DetectResult{}, errors.New("tunnel token is not valid base64")
		}
		return cloudflare.DetectResult{AccountID: "acct1", TunnelID: "tun1", Hostnames: []string{"a.example.org"},
			Connector: cloudflare.ConnectorStatus{Reachable: true, EdgeLocations: []string{}}}, nil
	}
	return f
}

func (f *networkFixture) createService(t *testing.T, body string) serviceDTO {
	t.Helper()
	code, resp := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", body)
	if code != http.StatusCreated {
		t.Fatalf("create service: %d %s", code, resp)
	}
	var dto serviceDTO
	if err := json.Unmarshal([]byte(resp), &dto); err != nil {
		t.Fatal(err)
	}
	return dto
}

func (f *networkFixture) createEndpoint(t *testing.T, body string) endpointDTO {
	t.Helper()
	code, resp := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/endpoints", body)
	if code != http.StatusCreated {
		t.Fatalf("create endpoint: %d %s", code, resp)
	}
	var dto endpointDTO
	if err := json.Unmarshal([]byte(resp), &dto); err != nil {
		t.Fatal(err)
	}
	return dto
}

func (f *networkFixture) connectCloudflare(t *testing.T) {
	t.Helper()
	code, body := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/cloudflare", `{"name":"lab","tunnel_token":"`+testTunnelToken("acct1", "tun1")+`"}`)
	if code != http.StatusOK {
		t.Fatalf("connect cloudflare: %d %s", code, body)
	}
}

const dockerServiceBody = `{"name":"portainer","driver":"docker","spec":{"docker":{"containers":["portainer"]}},"idle_timeout_seconds":600}`

func TestServiceCRUD(t *testing.T) {
	f := newNetworkFixture(t)
	created := f.createService(t, dockerServiceBody)
	if created.ID == "" || !strings.HasPrefix(created.ID, "svc") || created.Status.State != services.StateSleeping || created.IdleTimeoutSeconds != 600 {
		t.Fatalf("created = %+v", created)
	}
	if names := f.svc.appliedNames(); len(names) != 1 || names[0] != "portainer" {
		t.Fatalf("manager not reloaded after create: %v", names)
	}

	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", dockerServiceBody); code != http.StatusConflict || !strings.Contains(body, "already exists") {
		t.Fatalf("duplicate: %d %s", code, body)
	}

	invalid := []string{
		`{"name":"Bad Name","driver":"docker","spec":{"docker":{"containers":["x"]}}}`,
		`{"name":"x","driver":"docker","spec":{}}`,
		`{"name":"x","driver":"docker","spec":{"docker":{"containers":["x"]},"bogus":1}}`,
		`{"name":"x","driver":"podman","spec":{}}`,
		`{"name":"x","driver":"docker","spec":{"docker":{"containers":["x"]}},"idle_timeout_seconds":-1}`,
	}
	for _, body := range invalid {
		if code, resp := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, code, resp)
		}
	}

	code, body := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/services/"+created.ID, `{"name":"portainer-ce","driver":"compose","spec":{"compose":{"project":"portainer"}}}`)
	if code != http.StatusOK || !strings.Contains(body, `"name":"portainer-ce"`) || !strings.Contains(body, `"id":"`+created.ID+`"`) {
		t.Fatalf("update: %d %s", code, body)
	}
	if names := f.svc.appliedNames(); len(names) != 1 || names[0] != "portainer-ce" {
		t.Fatalf("manager not reloaded after update: %v", names)
	}

	code, body = statusAndBody(t, f.srv, http.MethodGet, "/api/v1/services/"+created.ID, "")
	if code != http.StatusOK || !strings.Contains(body, `"project":"portainer"`) {
		t.Fatalf("get: %d %s", code, body)
	}
	if code, _ := statusAndBody(t, f.srv, http.MethodGet, "/api/v1/services/svc_missing", ""); code != http.StatusNotFound {
		t.Fatalf("get missing: %d", code)
	}

	if code, _ := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+created.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if names := f.svc.appliedNames(); len(names) != 0 {
		t.Fatalf("manager still has %v after delete", names)
	}
	code, body = statusAndBody(t, f.srv, http.MethodGet, "/api/v1/services", "")
	if code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("list after delete: %d %s", code, body)
	}
}

func TestServiceHostCommandsGate(t *testing.T) {
	f := newNetworkFixture(t)
	execBody := `{"name":"backup","driver":"exec","spec":{"exec":{"start":["systemctl","start","backup"],"stop":["systemctl","stop","backup"]}}}`
	composeFileBody := `{"name":"stack","driver":"compose","spec":{"compose":{"project":"stack","file":"/srv/stack/compose.yml"}}}`
	execSvc := f.createService(t, execBody)
	if !strings.Contains(statusBody(t, f, http.MethodGet, "/api/v1/capabilities"), `"host_commands":true`) {
		t.Fatal("capabilities must report host commands when enabled")
	}

	f.api.SetHostCommands(false)
	if !strings.Contains(statusBody(t, f, http.MethodGet, "/api/v1/capabilities"), `"host_commands":false`) {
		t.Fatal("capabilities must report host commands disabled")
	}
	for _, body := range []string{execBody, composeFileBody} {
		if code, resp := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", body); code != http.StatusForbidden || !strings.Contains(resp, "MISHMESH_HOST_COMMANDS") {
			t.Fatalf("host command service with host commands off: %d %s", code, resp)
		}
	}
	downgrade := `{"name":"backup","driver":"docker","spec":{"docker":{"containers":["backup"]}}}`
	if code, resp := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/services/"+execSvc.ID, downgrade); code != http.StatusForbidden {
		t.Fatalf("editing an exec service without host access: %d %s", code, resp)
	}
	if code, resp := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+execSvc.ID, ""); code != http.StatusForbidden {
		t.Fatalf("deleting an exec service without host access: %d %s", code, resp)
	}
	plain := f.createService(t, `{"name":"plain","driver":"compose","spec":{"compose":{"project":"plain"}}}`)
	if code, resp := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+plain.ID, ""); code != http.StatusNoContent {
		t.Fatalf("deleting a plain service without host access: %d %s", code, resp)
	}
	f.api.SetHostCommands(true)
	if code, resp := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+execSvc.ID, ""); code != http.StatusNoContent {
		t.Fatalf("deleting an exec service with host access: %d %s", code, resp)
	}
}

func statusBody(t *testing.T, f *networkFixture, method, path string) string {
	t.Helper()
	code, body := statusAndBody(t, f.srv, method, path, "")
	if code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, code, body)
	}
	return body
}

func TestServiceLinkingAndControl(t *testing.T) {
	f := newNetworkFixture(t)
	svc := f.createService(t, dockerServiceBody)
	ep := f.createEndpoint(t, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:9000"],"service_id":"`+svc.ID+`"}}`)

	code, body := statusAndBody(t, f.srv, http.MethodGet, "/api/v1/services/"+svc.ID, "")
	if code != http.StatusOK || !strings.Contains(body, `"endpoints":["`+ep.ID+`"]`) {
		t.Fatalf("linked endpoints: %d %s", code, body)
	}

	code, body = statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+svc.ID, "")
	if code != http.StatusConflict || !strings.Contains(body, ep.ID) {
		t.Fatalf("delete while linked: %d %s", code, body)
	}

	code, body = statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/"+svc.ID+"/start", "")
	if code != http.StatusOK || !strings.Contains(body, `"state":"running"`) {
		t.Fatalf("start: %d %s", code, body)
	}
	if len(f.svc.ensured) != 1 || f.svc.ensured[0] != svc.ID+"@93.184.216.34:9000" {
		t.Fatalf("ensured = %v", f.svc.ensured)
	}
	code, body = statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/"+svc.ID+"/stop", "")
	if code != http.StatusOK || !strings.Contains(body, `"state":"sleeping"`) {
		t.Fatalf("stop: %d %s", code, body)
	}
	f.svc.startErr = errors.New("docker unreachable")
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/"+svc.ID+"/start", ""); code != http.StatusBadGateway {
		t.Fatalf("failed start: %d %s", code, body)
	}
	if code, _ := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/svc_missing/start", ""); code != http.StatusNotFound {
		t.Fatalf("start missing: %d", code)
	}

	if code, body := statusAndBody(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"policy":{"proxy_targets":["93.184.216.34:9000"]}}`); code != http.StatusOK {
		t.Fatalf("unlink: %d %s", code, body)
	}
	if code, body := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+svc.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete after unlink: %d %s", code, body)
	}
}

func TestServiceControlWithoutManager(t *testing.T) {
	api, srv := newNetworkTestAPI(t)
	api.SetHostCommands(true)
	code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/services", dockerServiceBody)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var svc serviceDTO
	_ = json.Unmarshal([]byte(body), &svc)
	if svc.Status.State != services.StateUnknown {
		t.Fatalf("state without manager = %q", svc.Status.State)
	}
	if code, _ := statusAndBody(t, srv, http.MethodPost, "/api/v1/services/"+svc.ID+"/start", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("start without manager: %d", code)
	}
}

func TestServiceInvalidRecordIsSkipped(t *testing.T) {
	f := newNetworkFixture(t)
	ctx := context.Background()
	good := f.createService(t, dockerServiceBody)
	bad := &store.Service{ID: store.NewID("svc"), OrgID: defaultOrgID, Name: "broken", Driver: "docker", Spec: json.RawMessage(`{"nope":1}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.api.data.CreateService(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if err := f.api.LoadServices(ctx); err != nil {
		t.Fatal(err)
	}
	if names := f.svc.appliedNames(); len(names) != 1 || names[0] != good.Name {
		t.Fatalf("applied = %v", names)
	}
	code, body := statusAndBody(t, f.srv, http.MethodGet, "/api/v1/services/"+bad.ID, "")
	if code != http.StatusOK || !strings.Contains(body, `"state":"error"`) {
		t.Fatalf("broken service: %d %s", code, body)
	}
	if code, _ := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/"+bad.ID+"/start", ""); code != http.StatusConflict {
		t.Fatalf("start unloaded: %d", code)
	}
}

func TestNetworkPolicyValidation(t *testing.T) {
	tests := []struct {
		name     string
		connect  bool
		body     string
		wantCode int
		wantMsg  string
	}{
		{"service of this org", false, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service_id":"$SVC"}}`, http.StatusCreated, ""},
		{"service unknown", false, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service_id":"svc_nope"}}`, http.StatusBadRequest, "not a service of this org"},
		{"service of another org", false, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service_id":"$OTHER"}}`, http.StatusBadRequest, "not a service of this org"},
		{"public access without connection", false, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"cloudflare"}}`, http.StatusBadRequest, "needs a Cloudflare connection"},
		{"public access without domain", true, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"cloudflare"}}`, http.StatusBadRequest, "requires a custom domain"},
		{"public access unknown", true, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"akamai"}}`, http.StatusBadRequest, "public_access must be"},
		{"public access with domain", true, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"cloudflare"}}`, http.StatusCreated, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newNetworkFixture(t)
			ctx := context.Background()
			svc := f.createService(t, dockerServiceBody)
			if err := f.api.data.CreateOrg(ctx, &store.Org{ID: "org_other", Name: "other", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			other := &store.Service{ID: store.NewID("svc"), OrgID: "org_other", Name: "hidden", Driver: "docker", Spec: json.RawMessage(`{"docker":{"containers":["h"]}}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if err := f.api.data.CreateService(ctx, other); err != nil {
				t.Fatal(err)
			}
			if tc.connect {
				f.connectCloudflare(t)
			}
			body := strings.NewReplacer("$SVC", svc.ID, "$OTHER", other.ID).Replace(tc.body)
			before := f.cf.triggerCount(defaultOrgID)
			code, resp := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/endpoints", body)
			if code != tc.wantCode {
				t.Fatalf("status %d want %d: %s", code, tc.wantCode, resp)
			}
			if tc.wantMsg != "" && !strings.Contains(resp, tc.wantMsg) {
				t.Fatalf("body %q missing %q", resp, tc.wantMsg)
			}
			if triggered := f.cf.triggerCount(defaultOrgID) > before; triggered != (code == http.StatusCreated) {
				t.Fatalf("triggered=%v for status %d", triggered, code)
			}
		})
	}
}

func TestPatchRevalidatesNetworkPolicy(t *testing.T) {
	f := newNetworkFixture(t)
	f.connectCloudflare(t)
	ep := f.createEndpoint(t, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"cloudflare"}}`)
	code, body := statusAndBody(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"domain":""}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "requires a custom domain") {
		t.Fatalf("removing the domain of a public endpoint: %d %s", code, body)
	}
	before := f.cf.triggerCount(defaultOrgID)
	if code, _ := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/endpoints/"+ep.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if f.cf.triggerCount(defaultOrgID) <= before {
		t.Fatal("delete did not trigger a cloudflare sync")
	}
}

func TestPublicEndpointStaysEditableAfterDisconnect(t *testing.T) {
	f := newNetworkFixture(t)
	f.connectCloudflare(t)
	public := f.createEndpoint(t, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"cloudflare"}}`)
	private := f.createEndpoint(t, `{"method":"proxy","domain":"other.example.org","policy":{"proxy_targets":["93.184.216.34:80"]}}`)
	if code, _ := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/cloudflare", ""); code != http.StatusNoContent {
		t.Fatalf("disconnect: %d", code)
	}
	cases := []struct {
		name string
		id   string
		body string
		want int
	}{
		{"edit targets of a public endpoint", public.ID, `{"policy":{"proxy_targets":["93.184.216.35:80"],"public_access":"cloudflare"}}`, http.StatusOK},
		{"edit port of a public endpoint", public.ID, `{"port":8443}`, http.StatusOK},
		{"turn public access off", public.ID, `{"policy":{"proxy_targets":["93.184.216.35:80"],"public_access":""}}`, http.StatusOK},
		{"turn public access back on", public.ID, `{"policy":{"proxy_targets":["93.184.216.35:80"],"public_access":"cloudflare"}}`, http.StatusBadRequest},
		{"newly public endpoint", private.ID, `{"policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"cloudflare"}}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := statusAndBody(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+tc.id, tc.body); code != tc.want {
				t.Fatalf("%d %s, want %d", code, body, tc.want)
			}
		})
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","domain":"new.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"public_access":"cloudflare"}}`); code != http.StatusBadRequest {
		t.Fatalf("create public endpoint without a connection: %d %s", code, body)
	}
}

func TestCloudflareConnectionLifecycle(t *testing.T) {
	f := newNetworkFixture(t)
	code, body := statusAndBody(t, f.srv, http.MethodGet, "/api/v1/cloudflare", "")
	if code != http.StatusOK || !strings.Contains(body, `"level":"none"`) || strings.Contains(body, `"connection"`) || !strings.Contains(body, `"status"`) {
		t.Fatalf("empty: %d %s", code, body)
	}
	if code, _ := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/cloudflare/sync", ""); code != http.StatusNotFound {
		t.Fatalf("sync without connection: %d", code)
	}

	tunnelToken := testTunnelToken("acct1", "tun1")
	f.cf.setStatus(defaultOrgID, cloudflare.Status{Level: cloudflare.LevelImported, Hostnames: []cloudflare.HostStatus{{Host: "fresh.example.org", State: cloudflare.StateUnclaimed}}})
	syncsBefore := f.cf.syncCount(defaultOrgID)
	code, body = statusAndBody(t, f.srv, http.MethodPut, "/api/v1/cloudflare", `{"name":"lab","tunnel_token":"`+tunnelToken+`"}`)
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, body)
	}
	if f.cf.syncCount(defaultOrgID) != syncsBefore+1 || !strings.Contains(body, "fresh.example.org") {
		t.Fatalf("import must answer with a freshly synced status: syncs=%d->%d %s", syncsBefore, f.cf.syncCount(defaultOrgID), body)
	}
	var view cloudflareDTO
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	c := view.Connection
	if view.Level != cloudflare.LevelImported || c == nil || c.AccountID != "acct1" || c.TunnelID != "tun1" || !c.TunnelTokenSet || c.APITokenSet ||
		c.Connector != cloudflare.ConnectorExternal || c.MetricsAddr != cloudflare.DefaultMetricsAddr || c.Origin != "http://127.0.0.1:8090" {
		t.Fatalf("imported view = %+v / %+v", view, c)
	}
	if strings.Contains(body, tunnelToken) {
		t.Fatal("response leaks the tunnel token")
	}
	if f.cf.reloadCount(defaultOrgID) != 1 {
		t.Fatalf("reloads = %d", f.cf.reloadCount(defaultOrgID))
	}
	stored, err := f.api.data.GetCloudflareConnection(context.Background(), defaultOrgID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.TunnelTokenEnc), tunnelToken) {
		t.Fatal("tunnel token stored in plaintext")
	}
	plain, err := f.api.secretBox.Open(stored.TunnelTokenEnc, cloudflare.TokenContext(defaultOrgID, cloudflare.TokenKindTunnel))
	if err != nil || string(plain) != tunnelToken {
		t.Fatalf("sealed tunnel token does not open: %v", err)
	}

	code, body = statusAndBody(t, f.srv, http.MethodPut, "/api/v1/cloudflare", `{"name":"lab","api_token":"cf-api-secret","prune":true,"access_app_id":"app-1"}`)
	if code != http.StatusOK || !strings.Contains(body, `"level":"managed"`) || !strings.Contains(body, `"api_token_set":true`) || strings.Contains(body, "cf-api-secret") {
		t.Fatalf("upgrade to managed: %d %s", code, body)
	}
	code, body = statusAndBody(t, f.srv, http.MethodPut, "/api/v1/cloudflare", `{"name":"lab","clear_api_token":true}`)
	if code != http.StatusOK || !strings.Contains(body, `"level":"imported"`) {
		t.Fatalf("downgrade to imported: %d %s", code, body)
	}

	f.cf.setStatus(defaultOrgID, cloudflare.Status{Level: cloudflare.LevelImported, Hostnames: []cloudflare.HostStatus{{Host: "a.example.org", State: cloudflare.StateUnclaimed}}})
	code, body = statusAndBody(t, f.srv, http.MethodPost, "/api/v1/cloudflare/sync", "")
	if code != http.StatusOK || !strings.Contains(body, "a.example.org") {
		t.Fatalf("sync: %d %s", code, body)
	}
	f.cf.syncErr = errors.New("connector unreachable")
	code, body = statusAndBody(t, f.srv, http.MethodPost, "/api/v1/cloudflare/sync", "")
	if code != http.StatusBadGateway || !strings.Contains(body, "connector unreachable") || !strings.Contains(body, `"status"`) {
		t.Fatalf("failed sync: %d %s", code, body)
	}

	if code, _ := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/cloudflare", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/cloudflare", ""); code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
	code, body = statusAndBody(t, f.srv, http.MethodGet, "/api/v1/cloudflare", "")
	if code != http.StatusOK || !strings.Contains(body, `"level":"none"`) {
		t.Fatalf("after delete: %d %s", code, body)
	}
}

func TestCloudflareConnectionValidation(t *testing.T) {
	token := testTunnelToken("acct1", "tun1")
	tests := []struct {
		name string
		body string
		want int
		msg  string
	}{
		{"missing name", `{"tunnel_token":"` + token + `"}`, http.StatusBadRequest, "name is required"},
		{"missing token", `{"name":"lab"}`, http.StatusBadRequest, "tunnel_token is required"},
		{"bad token", `{"name":"lab","tunnel_token":"bogus!"}`, http.StatusBadRequest, "base64"},
		{"bad token ids", `{"name":"lab","tunnel_token":"` + testTunnelToken("acct/1", "tun1") + `"}`, http.StatusBadRequest, "invalid account"},
		{"remote metrics", `{"name":"lab","tunnel_token":"` + token + `","metrics_addr":"10.0.0.1:20241"}`, http.StatusBadRequest, "loopback"},
		{"bad origin", `{"name":"lab","tunnel_token":"` + token + `","origin":"ftp://x"}`, http.StatusBadRequest, "origin must be"},
		{"origin with path", `{"name":"lab","tunnel_token":"` + token + `","origin":"http://127.0.0.1:8090/x"}`, http.StatusBadRequest, "path"},
		{"bad connector", `{"name":"lab","tunnel_token":"` + token + `","connector":"docker"}`, http.StatusBadRequest, "connector must be"},
		{"bad access app", `{"name":"lab","tunnel_token":"` + token + `","access_app_id":"../x"}`, http.StatusBadRequest, "access_app_id"},
		{"prune without api token", `{"name":"lab","tunnel_token":"` + token + `","prune":true}`, http.StatusBadRequest, "prune needs an API token"},
		{"clear and set api token", `{"name":"lab","tunnel_token":"` + token + `","api_token":"x","clear_api_token":true}`, http.StatusBadRequest, "mutually exclusive"},
		{"oversized token", `{"name":"lab","tunnel_token":"` + strings.Repeat("a", maxCloudflareTokenLen+1) + `"}`, http.StatusBadRequest, "too long"},
		{"custom origin", `{"name":"lab","tunnel_token":"` + token + `","origin":"https://ingress.lab:8443/"}`, http.StatusOK, `"origin":"https://ingress.lab:8443"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newNetworkFixture(t)
			code, body := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/cloudflare", tc.body)
			if code != tc.want || !strings.Contains(body, tc.msg) {
				t.Fatalf("%d %s, want %d containing %q", code, body, tc.want, tc.msg)
			}
		})
	}
}

func TestCloudflareTunnelBelongsToOneOrg(t *testing.T) {
	f := newNetworkFixture(t)
	ctx := context.Background()
	if err := f.api.data.CreateOrg(ctx, &store.Org{ID: "org_other", Name: "other", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.api.data.SetCloudflareConnection(ctx, &store.CloudflareConnection{OrgID: "org_other", Name: "theirs", AccountID: "acct1", TunnelID: "tun1",
		TunnelTokenEnc: []byte("x"), Connector: cloudflare.ConnectorExternal, MetricsAddr: cloudflare.DefaultMetricsAddr, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	code, body := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/cloudflare", `{"name":"lab","tunnel_token":"`+testTunnelToken("acct1", "tun1")+`"}`)
	if code != http.StatusConflict || !strings.Contains(body, "another organization") {
		t.Fatalf("cross-org tunnel: %d %s", code, body)
	}
}

func TestCloudflareWritesNeedHostAccess(t *testing.T) {
	f := newNetworkFixture(t)
	f.connectCloudflare(t)
	f.api.SetHostCommands(false)
	if code, body := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/cloudflare", `{"name":"lab"}`); code != http.StatusForbidden {
		t.Fatalf("put without host access: %d %s", code, body)
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/cloudflare/detect", `{}`); code != http.StatusForbidden {
		t.Fatalf("detect without host access: %d %s", code, body)
	}
	if code, body := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/cloudflare", ""); code != http.StatusNoContent {
		t.Fatalf("delete must not need host access: %d %s", code, body)
	}

	api, srv := newNetworkTestAPI(t)
	api.SetHostCommands(true)
	if code, body := statusAndBody(t, srv, http.MethodPut, "/api/v1/cloudflare", `{"name":"lab","tunnel_token":"`+testTunnelToken("a", "b")+`"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("put without a secret box: %d %s", code, body)
	}
}

func TestCloudflareDetect(t *testing.T) {
	f := newNetworkFixture(t)
	code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/cloudflare/detect", `{"tunnel_token":"x"}`)
	if code != http.StatusOK || !strings.Contains(body, `"tunnel_id":"tun1"`) || !strings.Contains(body, "a.example.org") {
		t.Fatalf("detect: %d %s", code, body)
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/cloudflare/detect", `{"metrics_addr":"8.8.8.8:53"}`); code != http.StatusBadRequest {
		t.Fatalf("detect remote: %d %s", code, body)
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/cloudflare/detect", `{"tunnel_token":"bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("detect bad token: %d %s", code, body)
	}
}

func TestEndpointHealthAndTopology(t *testing.T) {
	f := newNetworkFixture(t)
	h := &fakeHealth{results: map[string]health.EndpointHealth{}}
	f.api.SetHealth(h)
	f.connectCloudflare(t)
	svc := f.createService(t, `{"name":"syncthing","driver":"compose","spec":{"compose":{"project":"syncthing"}}}`)
	ep := f.createEndpoint(t, `{"method":"proxy","domain":"sync.example.org","policy":{"proxy_targets":["93.184.216.34:8384","https://93.184.216.35:8384"],"service_id":"`+svc.ID+`","public_access":"cloudflare"}}`)
	f.cf.setStatus(defaultOrgID, cloudflare.Status{
		Level:  cloudflare.LevelImported,
		Tunnel: &cloudflare.TunnelInfo{Name: "lab", Status: cloudflare.TunnelHealthy, Connections: 2},
		Hostnames: []cloudflare.HostStatus{
			{Host: "sync.example.org", EndpointID: ep.ID, State: cloudflare.StatePublished},
			{Host: "spare.example.org", State: cloudflare.StateUnclaimed, Detail: "no endpoint claims this hostname"},
		},
	})
	h.mu.Lock()
	h.results[ep.ID] = health.EndpointHealth{State: health.StateDegraded, Targets: []health.TargetStatus{
		{Target: "93.184.216.34:8384", Healthy: true},
		{Target: "https://93.184.216.35:8384", Error: "x509: certificate signed by unknown authority"},
	}}
	h.mu.Unlock()

	code, body := statusAndBody(t, f.srv, http.MethodGet, "/api/v1/endpoints/"+ep.ID, "")
	if code != http.StatusOK || !strings.Contains(body, `"health":{"state":"degraded"`) {
		t.Fatalf("endpoint health: %d %s", code, body)
	}

	topo := fetchTopology(t, f.srv)
	nodes := map[string]topologyNode{}
	for _, n := range topo.Nodes {
		nodes[n.ID] = n
	}
	want := map[string]string{
		tunnelNodeID:                        health.StateUp,
		"hostname:sync.example.org":         health.StateUp,
		"hostname:spare.example.org":        health.StateUnknown,
		"endpoint:" + ep.ID:                 health.StateDegraded,
		"target:http://93.184.216.34:8384":  health.StateUp,
		"target:https://93.184.216.35:8384": health.StateDown,
		"service:" + svc.ID:                 health.StateSleeping,
	}
	for id, st := range want {
		if n, ok := nodes[id]; !ok || n.State != st {
			t.Fatalf("node %s = %+v, want state %q (all: %v)", id, n, st, nodes)
		}
	}
	refs := map[string]topologyRef{
		tunnelNodeID:                {Kind: "cloudflare"},
		"hostname:sync.example.org": {Kind: "cloudflare"},
		"endpoint:" + ep.ID:         {Kind: "endpoint", ID: ep.ID},
		"service:" + svc.ID:         {Kind: "service", ID: svc.ID},
	}
	for id, ref := range refs {
		if n := nodes[id]; n.Ref == nil || *n.Ref != ref {
			t.Fatalf("node %s ref = %+v want %+v", id, n.Ref, ref)
		}
	}
	if nodes["service:"+svc.ID].Label != "syncthing" || nodes[tunnelNodeID].Label != "Cloudflare tunnel lab" {
		t.Fatalf("labels: %q %q", nodes["service:"+svc.ID].Label, nodes[tunnelNodeID].Label)
	}
	edges := map[topologyEdge]bool{}
	for _, e := range topo.Edges {
		edges[e] = true
	}
	for _, e := range []topologyEdge{
		{From: tunnelNodeID, To: "hostname:sync.example.org", Kind: edgeRoutes},
		{From: tunnelNodeID, To: "hostname:spare.example.org", Kind: edgeRoutes},
		{From: "hostname:sync.example.org", To: "endpoint:" + ep.ID, Kind: edgeServes},
		{From: "endpoint:" + ep.ID, To: "target:http://93.184.216.34:8384", Kind: edgeProxies},
		{From: "endpoint:" + ep.ID, To: "service:" + svc.ID, Kind: edgeWakes},
	} {
		if !edges[e] {
			t.Fatalf("missing edge %+v in %v", e, topo.Edges)
		}
	}

	h.mu.Lock()
	h.results[ep.ID] = health.EndpointHealth{State: health.StateSleeping, Detail: "service syncthing is scaled to zero", Targets: []health.TargetStatus{
		{Target: "93.184.216.34:8384", Error: "connection refused"},
	}}
	h.mu.Unlock()
	for _, n := range fetchTopology(t, f.srv).Nodes {
		if n.ID == "target:http://93.184.216.34:8384" && n.State != health.StateSleeping {
			t.Fatalf("target behind a sleeping service is %q, want sleeping", n.State)
		}
	}

	if code, _ := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/endpoints/"+ep.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.forgot) != 1 || h.forgot[0] != ep.ID {
		t.Fatalf("forgot = %v", h.forgot)
	}
}

func TestTopologyLinksUnclaimedHostnameToPrivateEndpoint(t *testing.T) {
	f := newNetworkFixture(t)
	f.connectCloudflare(t)
	ep := f.createEndpoint(t, `{"method":"proxy","domain":"wiki.example.org","policy":{"proxy_targets":["93.184.216.34:80"]}}`)
	f.cf.setStatus(defaultOrgID, cloudflare.Status{
		Level:  cloudflare.LevelImported,
		Tunnel: &cloudflare.TunnelInfo{Name: "lab", Status: cloudflare.TunnelHealthy},
		Hostnames: []cloudflare.HostStatus{
			{Host: "wiki.example.org", EndpointID: ep.ID, State: cloudflare.StateUnclaimed},
			{Host: "spare.example.org", State: cloudflare.StateUnclaimed},
		},
	})
	topo := fetchTopology(t, f.srv)
	refs := map[string]*topologyRef{}
	for _, n := range topo.Nodes {
		refs[n.ID] = n.Ref
	}
	if r := refs["hostname:wiki.example.org"]; r == nil || *r != (topologyRef{Kind: "endpoint", ID: ep.ID}) {
		t.Fatalf("matched hostname ref = %+v", r)
	}
	if r := refs["hostname:spare.example.org"]; r == nil || *r != (topologyRef{Kind: "cloudflare"}) {
		t.Fatalf("orphan hostname ref = %+v", r)
	}
	found := false
	for _, e := range topo.Edges {
		if e == (topologyEdge{From: "hostname:wiki.example.org", To: "endpoint:" + ep.ID, Kind: edgeServes}) {
			found = true
		}
		if e.From == "hostname:spare.example.org" {
			t.Fatalf("orphan hostname has an outgoing edge %+v", e)
		}
	}
	if !found {
		t.Fatalf("missing hostname -> endpoint edge in %v", topo.Edges)
	}
}

func TestTopologyWithoutCloudflare(t *testing.T) {
	f := newNetworkFixture(t)
	ep := f.createEndpoint(t, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service_id":"`+f.createService(t, dockerServiceBody).ID+`"}}`)
	for _, n := range fetchTopology(t, f.srv).Nodes {
		if n.Kind == nodeTunnel || n.Kind == nodeHostname {
			t.Fatalf("unexpected cloudflare node %+v for endpoint %s", n, ep.ID)
		}
	}
}

func fetchTopology(t *testing.T, srv *httptest.Server) topologyDTO {
	t.Helper()
	code, body := statusAndBody(t, srv, http.MethodGet, "/api/v1/topology", "")
	if code != http.StatusOK {
		t.Fatalf("topology: %d %s", code, body)
	}
	var topo topologyDTO
	if err := json.Unmarshal([]byte(body), &topo); err != nil {
		t.Fatal(err)
	}
	return topo
}

func TestNetworkRolePermissions(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("localhost:8080", "http")
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true})
	api.SetServices(newFakeServices())
	api.SetCloudflare(newFakeCloudflare(), testSecretBox(t))
	api.SetHostCommands(true)
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	owner := registerClient(t, srv, "owner@example.com", "owner")
	admin := inviteAndRegister(t, srv, owner, "admin@example.com", "admin")
	member := inviteAndRegister(t, srv, owner, "member@example.com", "member")

	execBody := `{"name":"backup","driver":"exec","spec":{"exec":{"start":["true"],"stop":["true"],"env":{"TOKEN":"s3cret"}}}}`
	cfBody := `{"name":"lab","tunnel_token":"` + testTunnelToken("acct1", "tun1") + `"}`

	var caps map[string]bool
	doc(t, owner, srv, http.MethodGet, "/api/v1/capabilities", "", http.StatusOK, &caps)
	if !caps["host_commands"] {
		t.Fatal("owner must have host commands")
	}
	doc(t, admin, srv, http.MethodGet, "/api/v1/capabilities", "", http.StatusOK, &caps)
	if caps["host_commands"] {
		t.Fatal("admin must not have host commands")
	}

	doc(t, admin, srv, http.MethodPost, "/api/v1/services", execBody, http.StatusForbidden, nil)
	doc(t, admin, srv, http.MethodPut, "/api/v1/cloudflare", cfBody, http.StatusForbidden, nil)
	var svc serviceDTO
	doc(t, admin, srv, http.MethodPost, "/api/v1/services", dockerServiceBody, http.StatusCreated, &svc)
	var execSvc serviceDTO
	doc(t, owner, srv, http.MethodPost, "/api/v1/services", execBody, http.StatusCreated, &execSvc)
	doc(t, admin, srv, http.MethodPut, "/api/v1/services/"+execSvc.ID, dockerServiceBody, http.StatusForbidden, nil)
	doc(t, owner, srv, http.MethodPut, "/api/v1/cloudflare", cfBody, http.StatusOK, nil)

	doc(t, member, srv, http.MethodGet, "/api/v1/services", "", http.StatusOK, nil)
	doc(t, member, srv, http.MethodGet, "/api/v1/services/"+svc.ID, "", http.StatusOK, nil)
	doc(t, member, srv, http.MethodGet, "/api/v1/cloudflare", "", http.StatusOK, nil)
	doc(t, member, srv, http.MethodPost, "/api/v1/services", dockerServiceBody, http.StatusForbidden, nil)
	doc(t, member, srv, http.MethodPost, "/api/v1/services/"+svc.ID+"/start", "", http.StatusForbidden, nil)
	doc(t, member, srv, http.MethodDelete, "/api/v1/cloudflare", "", http.StatusForbidden, nil)
	doc(t, member, srv, http.MethodPost, "/api/v1/cloudflare/sync", "", http.StatusForbidden, nil)
	doc(t, admin, srv, http.MethodDelete, "/api/v1/cloudflare", "", http.StatusNoContent, nil)

	for _, tc := range []struct {
		name   string
		client *http.Client
		want   string
	}{
		{"owner", owner, "s3cret"},
		{"admin", admin, redactedEnvValue},
		{"member", member, redactedEnvValue},
	} {
		var got serviceDTO
		doc(t, tc.client, srv, http.MethodGet, "/api/v1/services/"+execSvc.ID, "", http.StatusOK, &got)
		if got.Spec.Exec == nil || got.Spec.Exec.Env["TOKEN"] != tc.want {
			t.Errorf("%s sees exec env %+v, want TOKEN=%q", tc.name, got.Spec.Exec, tc.want)
		}
		var list []serviceDTO
		doc(t, tc.client, srv, http.MethodGet, "/api/v1/services", "", http.StatusOK, &list)
		for _, item := range list {
			if item.ID == execSvc.ID && item.Spec.Exec.Env["TOKEN"] != tc.want {
				t.Errorf("%s list sees exec env %+v, want TOKEN=%q", tc.name, item.Spec.Exec, tc.want)
			}
		}
	}
}
