package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

type fakeServiceControl struct {
	mu       sync.Mutex
	statuses map[string]services.Status
	orgs     map[string]string
	ensured  []string
	stopped  []string
	startErr error
}

func (f *fakeServiceControl) VisibleTo(orgID, name string) bool {
	org, ok := f.orgs[name]
	return ok && org == orgID
}

func (f *fakeServiceControl) List() []services.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]services.Status, 0, len(f.statuses))
	for _, name := range []string{"portainer", "syncthing", "hidden"} {
		if st, ok := f.statuses[name]; ok {
			out = append(out, st)
		}
	}
	return out
}

func (f *fakeServiceControl) Status(name string) (services.Status, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.statuses[name]
	return st, ok
}

func (f *fakeServiceControl) EnsureRunning(_ context.Context, name, ready string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, name+"@"+ready)
	if f.startErr != nil {
		return f.startErr
	}
	st := f.statuses[name]
	st.State = services.StateRunning
	f.statuses[name] = st
	return nil
}

func (f *fakeServiceControl) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	st := f.statuses[name]
	st.State = services.StateSleeping
	f.statuses[name] = st
	return nil
}

func newFakeServices() *fakeServiceControl {
	return &fakeServiceControl{
		statuses: map[string]services.Status{
			"portainer": {Name: "portainer", Driver: "docker", State: services.StateRunning},
			"syncthing": {Name: "syncthing", Driver: "compose", State: services.StateSleeping},
			"hidden":    {Name: "hidden", Driver: "exec", State: services.StateRunning},
		},
		orgs: map[string]string{"portainer": defaultOrgID, "syncthing": defaultOrgID, "hidden": "org_other"},
	}
}

type fakeFrontDoor struct {
	mu       sync.Mutex
	orgs     map[string]bool
	status   cloudflare.Status
	triggers int
	syncErr  error
}

func (f *fakeFrontDoor) Enabled(orgID string) bool { return f.orgs[orgID] }
func (f *fakeFrontDoor) Status() cloudflare.Status { return f.status }
func (f *fakeFrontDoor) Sync(context.Context) (cloudflare.Status, error) {
	return f.status, f.syncErr
}
func (f *fakeFrontDoor) Trigger() {
	f.mu.Lock()
	f.triggers++
	f.mu.Unlock()
}

func (f *fakeFrontDoor) triggerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers
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

func TestNetworkPolicyValidation(t *testing.T) {
	tests := []struct {
		name        string
		withSvc     bool
		withFD      bool
		body        string
		wantCode    int
		wantMsg     string
		wantTrigger bool
	}{
		{"service without services file", false, false, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service":"portainer"}}`, http.StatusBadRequest, "MISHMESH_SERVICES_FILE", false},
		{"service configured", true, false, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service":"portainer"}}`, http.StatusCreated, "", false},
		{"service unknown", true, false, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service":"nope"}}`, http.StatusBadRequest, "not configured for this org", false},
		{"service of another org", true, false, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"service":"hidden"}}`, http.StatusBadRequest, "not configured for this org", false},
		{"front door disabled", false, false, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"front_door":"cloudflare"}}`, http.StatusBadRequest, "not enabled", false},
		{"front door without domain", false, true, `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"front_door":"cloudflare"}}`, http.StatusBadRequest, "requires a custom domain", false},
		{"front door unknown", false, true, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"front_door":"akamai"}}`, http.StatusBadRequest, "front_door must be", false},
		{"front door with domain", false, true, `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"front_door":"cloudflare"}}`, http.StatusCreated, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api, srv := newNetworkTestAPI(t)
			fd := &fakeFrontDoor{orgs: map[string]bool{defaultOrgID: true}}
			if tc.withSvc {
				api.SetServices(newFakeServices())
			}
			if tc.withFD {
				api.SetFrontDoor(fd)
			}
			code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/endpoints", tc.body)
			if code != tc.wantCode {
				t.Fatalf("status %d want %d: %s", code, tc.wantCode, body)
			}
			if tc.wantMsg != "" && !strings.Contains(body, tc.wantMsg) {
				t.Fatalf("body %q missing %q", body, tc.wantMsg)
			}
			if tc.wantTrigger != (fd.triggerCount() > 0) {
				t.Fatalf("trigger count %d, want trigger=%v", fd.triggerCount(), tc.wantTrigger)
			}
		})
	}
}

func newNetworkTestAPI(t *testing.T) (*API, *httptest.Server) {
	t.Helper()
	api, srv := newTestAPI(t)
	_ = api.data.CreateOrg(context.Background(), &store.Org{ID: defaultOrgID, Name: "default", CreatedAt: time.Now()})
	proxy.Register(context.Background(), api.data, api.conns, nil, nil)
	return api, srv
}

func TestPatchRevalidatesNetworkPolicy(t *testing.T) {
	api, srv := newNetworkTestAPI(t)
	fd := &fakeFrontDoor{orgs: map[string]bool{defaultOrgID: true}}
	api.SetFrontDoor(fd)
	var ep endpointDTO
	code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","domain":"app.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"front_door":"cloudflare"}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	_ = json.Unmarshal([]byte(body), &ep)
	code, body = statusAndBody(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"domain":""}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "requires a custom domain") {
		t.Fatalf("removing the domain of a front-door endpoint: %d %s", code, body)
	}
	before := fd.triggerCount()
	code, _ = statusAndBody(t, srv, http.MethodDelete, "/api/v1/endpoints/"+ep.ID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if fd.triggerCount() <= before {
		t.Fatal("delete did not trigger a front door sync")
	}
}

func TestServicesRoutes(t *testing.T) {
	api, srv := newNetworkTestAPI(t)
	svc := newFakeServices()
	api.SetServices(svc)
	code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:8384"],"service":"syncthing"}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created endpointDTO
	_ = json.Unmarshal([]byte(body), &created)

	code, body = statusAndBody(t, srv, http.MethodGet, "/api/v1/services", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	var list []serviceDTO
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d services, want the 2 visible to the org: %s", len(list), body)
	}
	for _, s := range list {
		if s.Name == "syncthing" && (len(s.Endpoints) != 1 || s.Endpoints[0] != created.ID) {
			t.Fatalf("syncthing endpoints = %v", s.Endpoints)
		}
	}

	code, body = statusAndBody(t, srv, http.MethodPost, "/api/v1/services/syncthing/start", "")
	if code != http.StatusOK || !strings.Contains(body, `"state":"running"`) {
		t.Fatalf("start: %d %s", code, body)
	}
	if len(svc.ensured) != 1 || svc.ensured[0] != "syncthing@93.184.216.34:8384" {
		t.Fatalf("ensured = %v", svc.ensured)
	}
	code, body = statusAndBody(t, srv, http.MethodPost, "/api/v1/services/syncthing/stop", "")
	if code != http.StatusOK || !strings.Contains(body, `"state":"sleeping"`) {
		t.Fatalf("stop: %d %s", code, body)
	}
	for _, path := range []string{"/api/v1/services/hidden/start", "/api/v1/services/nope/stop"} {
		if code, _ := statusAndBody(t, srv, http.MethodPost, path, ""); code != http.StatusNotFound {
			t.Fatalf("%s: %d want 404", path, code)
		}
	}
	svc.startErr = errors.New("docker unreachable")
	if code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/services/portainer/start", ""); code != http.StatusBadGateway {
		t.Fatalf("failed start: %d %s", code, body)
	}
}

func TestServicesListWithoutServicesFile(t *testing.T) {
	_, srv := newNetworkTestAPI(t)
	code, body := statusAndBody(t, srv, http.MethodGet, "/api/v1/services", "")
	if code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("list: %d %s", code, body)
	}
}

func TestFrontDoorRoutes(t *testing.T) {
	api, srv := newNetworkTestAPI(t)
	code, body := statusAndBody(t, srv, http.MethodGet, "/api/v1/frontdoors", "")
	if code != http.StatusOK || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("disabled: %d %s", code, body)
	}
	if code, _ := statusAndBody(t, srv, http.MethodPost, "/api/v1/frontdoors/cloudflare/sync", ""); code != http.StatusNotFound {
		t.Fatalf("sync when disabled: %d", code)
	}
	code, body = statusAndBody(t, srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"]}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var mine endpointDTO
	_ = json.Unmarshal([]byte(body), &mine)
	fd := &fakeFrontDoor{orgs: map[string]bool{defaultOrgID: true, "org_other": true}, status: cloudflare.Status{Enabled: true, Origin: "http://127.0.0.1:8090", Hostnames: []cloudflare.HostStatus{
		{Host: "a.example.org", EndpointID: mine.ID, State: "synced"},
		{Host: "other-tenant.example.org", EndpointID: "ep_other", State: "synced"},
	}}}
	api.SetFrontDoor(fd)
	code, body = statusAndBody(t, srv, http.MethodGet, "/api/v1/frontdoors", "")
	if code != http.StatusOK || !strings.Contains(body, "a.example.org") || strings.Contains(body, "other-tenant") {
		t.Fatalf("hostnames not scoped to the org: %d %s", code, body)
	}
	code, body = statusAndBody(t, srv, http.MethodPost, "/api/v1/frontdoors/cloudflare/sync", "")
	if code != http.StatusOK || !strings.Contains(body, "a.example.org") || strings.Contains(body, "other-tenant") {
		t.Fatalf("sync: %d %s", code, body)
	}
	fd.syncErr = errors.New("cloudflare api: 403")
	if code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/frontdoors/cloudflare/sync", ""); code != http.StatusBadGateway {
		t.Fatalf("failed sync: %d %s", code, body)
	}
	fd.orgs = map[string]bool{"org_other": true}
	code, body = statusAndBody(t, srv, http.MethodGet, "/api/v1/frontdoors", "")
	if code != http.StatusOK || strings.Contains(body, "a.example.org") {
		t.Fatalf("front door leaked to a non-allowlisted org: %s", body)
	}
}

func TestEndpointHealthAndTopology(t *testing.T) {
	api, srv := newNetworkTestAPI(t)
	h := &fakeHealth{results: map[string]health.EndpointHealth{}}
	api.SetHealth(h)
	api.SetServices(newFakeServices())
	fd := &fakeFrontDoor{orgs: map[string]bool{defaultOrgID: true}, status: cloudflare.Status{
		Enabled: true, Tunnel: &cloudflare.Tunnel{Name: "lab", Status: "healthy"},
	}}
	api.SetFrontDoor(fd)
	code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","domain":"sync.example.org","policy":{"proxy_targets":["93.184.216.34:8384","https://93.184.216.35:8384"],"service":"syncthing","front_door":"cloudflare"}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var ep endpointDTO
	_ = json.Unmarshal([]byte(body), &ep)
	fd.status.Hostnames = []cloudflare.HostStatus{{Host: "sync.example.org", EndpointID: ep.ID, State: "synced"}}
	h.mu.Lock()
	h.results[ep.ID] = health.EndpointHealth{State: health.StateDegraded, Targets: []health.TargetStatus{
		{Target: "93.184.216.34:8384", Healthy: true},
		{Target: "https://93.184.216.35:8384", Error: "x509: certificate signed by unknown authority"},
	}}
	h.mu.Unlock()

	code, body = statusAndBody(t, srv, http.MethodGet, "/api/v1/endpoints/"+ep.ID, "")
	if code != http.StatusOK || !strings.Contains(body, `"health":{"state":"degraded"`) {
		t.Fatalf("endpoint health: %d %s", code, body)
	}

	code, body = statusAndBody(t, srv, http.MethodGet, "/api/v1/topology", "")
	if code != http.StatusOK {
		t.Fatalf("topology: %d %s", code, body)
	}
	var topo topologyDTO
	if err := json.Unmarshal([]byte(body), &topo); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, n := range topo.Nodes {
		states[n.ID] = n.State
	}
	want := map[string]string{
		"frontdoor:cloudflare":              health.StateUp,
		"hostname:sync.example.org":         health.StateUp,
		"endpoint:" + ep.ID:                 health.StateDegraded,
		"target:http://93.184.216.34:8384":  health.StateUp,
		"target:https://93.184.216.35:8384": health.StateDown,
		"service:syncthing":                 health.StateSleeping,
	}
	for id, st := range want {
		if states[id] != st {
			t.Fatalf("node %s state %q want %q (all: %v)", id, states[id], st, states)
		}
	}
	edges := map[topologyEdge]bool{}
	for _, e := range topo.Edges {
		edges[e] = true
	}
	for _, e := range []topologyEdge{
		{From: "frontdoor:cloudflare", To: "hostname:sync.example.org", Kind: edgeRoutes},
		{From: "hostname:sync.example.org", To: "endpoint:" + ep.ID, Kind: edgeServes},
		{From: "endpoint:" + ep.ID, To: "target:http://93.184.216.34:8384", Kind: edgeProxies},
		{From: "endpoint:" + ep.ID, To: "service:syncthing", Kind: edgeWakes},
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
	_, body = statusAndBody(t, srv, http.MethodGet, "/api/v1/topology", "")
	topo = topologyDTO{}
	if err := json.Unmarshal([]byte(body), &topo); err != nil {
		t.Fatal(err)
	}
	for _, n := range topo.Nodes {
		if n.ID == "target:http://93.184.216.34:8384" && n.State != health.StateSleeping {
			t.Fatalf("target behind a sleeping service is %q, want sleeping", n.State)
		}
	}

	if code, _ := statusAndBody(t, srv, http.MethodDelete, "/api/v1/endpoints/"+ep.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.forgot) != 1 || h.forgot[0] != ep.ID {
		t.Fatalf("forgot = %v", h.forgot)
	}
}
