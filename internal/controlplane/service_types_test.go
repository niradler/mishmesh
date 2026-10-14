package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

func TestShouldExposeDriverDescriptorsAndTypedServiceRoundTrips(t *testing.T) {
	f := newNetworkFixture(t)
	code, body := statusAndBody(t, f.srv, http.MethodGet, "/api/v1/service-drivers", "")
	var descriptors []services.DriverDescriptor
	if code != http.StatusOK || json.Unmarshal([]byte(body), &descriptors) != nil || len(descriptors) != 3 {
		t.Fatalf("driver descriptors: %d %s", code, body)
	}
	for _, test := range []struct{ driver, config string }{
		{"docker", `{"containers":["web"]}`},
		{"compose", `{"project":"web"}`},
		{"command", `{"start":["true"],"stop":["true"]}`},
	} {
		for _, idle := range []int{-1, 0, 10} {
			input := serviceInput{Name: test.driver + "-" + store.NewID("test"), Driver: test.driver, Config: json.RawMessage(test.config), Ready: json.RawMessage(`{"tcp":"127.0.0.1:8080"}`), Requires: []string{}, IdleTimeoutSeconds: idle}
			raw, _ := json.Marshal(input)
			created := f.createService(t, string(raw))
			code, response := statusAndBody(t, f.srv, http.MethodGet, "/api/v1/services/"+created.ID, "")
			var got serviceDTO
			if code != http.StatusOK || json.Unmarshal([]byte(response), &got) != nil || got.Driver != test.driver || got.IdleTimeoutSeconds != idle || len(got.Config) == 0 || !strings.Contains(string(got.Ready), "127.0.0.1:8080") {
				t.Fatalf("typed round trip: %d %s", code, response)
			}
		}
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", `{"name":"legacy","driver":"docker","spec":{"docker":{"containers":["web"]}}}`); code != http.StatusBadRequest {
		t.Fatalf("legacy API shape accepted: %d %s", code, body)
	}
	for _, config := range []string{`{"containers":["web"],"containers":["replacement"]}`, `{"start":["true"],"stop":["true"],"env":{"TOKEN":"first","TOKEN":"second"}}`} {
		driver := "docker"
		if strings.Contains(config, "start") {
			driver = "command"
		}
		if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", `{"name":"duplicate","driver":"`+driver+`","config":`+config+`}`); code != http.StatusBadRequest {
			t.Fatalf("ambiguous typed config accepted: %d %s", code, body)
		}
	}
}

func TestShouldValidateRequiresAndExposeRequiredBy(t *testing.T) {
	f := newNetworkFixture(t)
	base := f.createService(t, `{"name":"base","driver":"docker","config":{"containers":["base"]}}`)
	dependent := f.createService(t, `{"name":"dependent","driver":"docker","config":{"containers":["dependent"]},"requires":["`+base.ID+`"]}`)
	ctx := context.Background()
	if err := f.api.data.CreateOrg(ctx, &store.Org{ID: "foreign-requirement-org", Name: "foreign-requirement-org"}); err != nil {
		t.Fatal(err)
	}
	if err := f.api.data.CreateService(ctx, &store.Service{ID: "foreign-requirement", OrgID: "foreign-requirement-org", Name: "private-service-name", Driver: "docker", Config: json.RawMessage(`{"containers":["private"]}`)}); err != nil {
		t.Fatal(err)
	}
	if body := statusBody(t, f, http.MethodGet, "/api/v1/services/"+base.ID); !strings.Contains(body, `"required_by":["`+dependent.ID+`"]`) {
		t.Fatalf("missing required_by: %s", body)
	}
	for _, requirement := range []string{"missing", "foreign-requirement", base.ID, dependent.ID} {
		body := `{"name":"base","driver":"docker","config":{"containers":["base"]},"requires":["` + requirement + `"]}`
		if code, response := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/services/"+base.ID, body); code != http.StatusBadRequest || strings.Contains(response, "private-service-name") {
			t.Fatalf("invalid requirement accepted: %d %s", code, response)
		}
	}
	if code, body := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+base.ID, ""); code != http.StatusConflict || !strings.Contains(body, dependent.ID) {
		t.Fatalf("delete dependency guard: %d %s", code, body)
	}
}

type connectionServiceType struct{}

func (connectionServiceType) Describe() services.DriverDescriptor {
	return services.DriverDescriptor{Type: "connected", Label: "Connected", Fields: []typedfields.Field{{Key: "network", Kind: "connection", Required: true, ConnType: "tailscale"}, {Key: "other_network", Kind: "connection", ConnType: "tailscale"}}}
}

func (d connectionServiceType) Validate(raw json.RawMessage) error {
	return typedfields.Validate(d.Describe().Fields, raw)
}

func (connectionServiceType) New(json.RawMessage, services.Deps) (services.Driver, error) {
	return nil, errors.New("descriptor test only")
}

func TestShouldResolveEveryDescriptorConnectionField(t *testing.T) {
	f := newNetworkFixture(t)
	registry := services.NewBuiltinRegistry()
	if err := registry.Register(connectionServiceType{}); err != nil {
		t.Fatal(err)
	}
	f.api.SetServiceRegistry(registry)
	ctx := context.Background()
	if err := f.api.data.CreateOrg(ctx, &store.Org{ID: "foreign", Name: "foreign"}); err != nil {
		t.Fatal(err)
	}
	for _, record := range []*store.Connection{
		{ID: "matching", OrgID: defaultOrgID, Name: "matching", Type: "tailscale", Config: json.RawMessage(`{}`)},
		{ID: "second", OrgID: defaultOrgID, Name: "second", Type: "tailscale", Config: json.RawMessage(`{}`)},
		{ID: "wrong", OrgID: defaultOrgID, Name: "wrong", Type: "cloudflare", Config: json.RawMessage(`{}`)},
		{ID: "foreign", OrgID: "foreign", Name: "private-name", Type: "tailscale", Config: json.RawMessage(`{}`)},
	} {
		if err := f.api.data.CreateConnection(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"wrong", "foreign", "missing"} {
		code, response := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", `{"name":"connected","driver":"connected","config":{"network":"matching","other_network":"`+id+`"}}`)
		if code != http.StatusBadRequest || strings.Contains(response, "private-name") {
			t.Fatalf("connection validation: %d %s", code, response)
		}
	}
	created := f.createService(t, `{"name":"connected","driver":"connected","config":{"network":"matching","other_network":"second"}}`)
	record, err := f.api.data.GetService(ctx, created.ID)
	if err != nil || len(record.ConnectionIDs) != 2 {
		t.Fatalf("descriptor references were not persisted: %+v %v", record, err)
	}
	if err := f.api.data.DeleteConnection(ctx, "second"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("connection delete guard=%v", err)
	}
}

func TestShouldPreserveSensitiveEnvironmentAndRejectInventedPlaceholders(t *testing.T) {
	f := newNetworkFixture(t)
	created := f.createService(t, `{"name":"command","driver":"command","config":{"start":["true"],"stop":["true"],"env":{"TOKEN":"sentinel"}}}`)
	for _, config := range []string{`{"start":["true"],"stop":["true"]}`, `{"start":["true"],"stop":["true"],"env":{"TOKEN":"<redacted>"}}`} {
		code, body := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/services/"+created.ID, `{"name":"command","driver":"command","config":`+config+`}`)
		if code != http.StatusOK {
			t.Fatalf("preserved edit: %d %s", code, body)
		}
		record, _ := f.api.data.GetService(context.Background(), created.ID)
		if !strings.Contains(string(record.Config), "sentinel") || strings.Contains(string(record.Config), "<redacted>") {
			t.Fatalf("lost original credential: %s", record.Config)
		}
	}
	code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", `{"name":"invented","driver":"command","config":{"start":["true"],"stop":["true"],"env":{"TOKEN":"<redacted>"}}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invented placeholder accepted: %d %s", code, body)
	}
}

type serviceLeaseIntercept struct {
	store.DataStore
	store.ServiceSnapshotStore
	once     sync.Once
	before   func()
	failSave bool
}

func (s *serviceLeaseIntercept) WithServiceSnapshots(ctx context.Context, orgID string, expected []*store.Service, action func(store.ServiceMutation) error) error {
	s.once.Do(func() {
		if s.before != nil {
			s.before()
		}
	})
	return s.ServiceSnapshotStore.WithServiceSnapshots(ctx, orgID, expected, func(mutation store.ServiceMutation) error {
		if s.failSave {
			return action(serviceFailSave{ServiceMutation: mutation})
		}
		return action(mutation)
	})
}

type serviceFailSave struct{ store.ServiceMutation }

func (serviceFailSave) UpdateService(context.Context, *store.Service) error {
	return errors.New("sentinel-save-error")
}

func TestShouldExposeRestartFailuresByCommittedPhase(t *testing.T) {
	for _, phase := range []string{"stop", "save", "start", "success"} {
		t.Run(phase, func(t *testing.T) {
			f := newNetworkFixture(t)
			created := f.createService(t, dockerServiceBody)
			statusBody(t, f, http.MethodPost, "/api/v1/services/"+created.ID+"/start")
			if phase == "stop" {
				f.svc.stopErr = errors.New("secret-sentinel-stop")
			}
			if phase == "start" {
				f.svc.startErr = errors.New("secret-sentinel-start")
			}
			if phase == "save" {
				f.api.data = &serviceLeaseIntercept{DataStore: f.api.data, ServiceSnapshotStore: f.api.data.(store.ServiceSnapshotStore), failSave: true}
			}
			body := `{"name":"new-name","driver":"docker","config":{"containers":["new-runtime"]}}`
			if code, response := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/services/"+created.ID, body); code != http.StatusConflict {
				t.Fatalf("silent runtime edit accepted: %d %s", code, response)
			}
			code, response := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/services/"+created.ID+"?restart=true", body)
			want := http.StatusBadGateway
			if phase == "success" {
				want = http.StatusOK
			}
			if code != want || strings.Contains(response, "secret-sentinel") || strings.Contains(response, "sentinel-save-error") {
				t.Fatalf("restart result: %d %s", code, response)
			}
			record, _ := f.api.data.GetService(context.Background(), created.ID)
			if phase == "start" || phase == "success" {
				if record.Name != "new-name" {
					t.Fatalf("new save lost after %s: %+v", phase, record)
				}
			} else if record.Name != "portainer" {
				t.Fatalf("failed %s overwrote old save: %+v", phase, record)
			}
			if phase != "success" && (!strings.Contains(response, `"phase":"`+phase+`"`) || !strings.Contains(response, `"saved":`+map[bool]string{true: "true", false: "false"}[phase == "start"])) {
				t.Fatalf("incorrect phase/save report: %s", response)
			}
		})
	}
}

func TestShouldRejectStaleAuthorizedServiceActionsBeforeRuntimeEffects(t *testing.T) {
	for _, action := range []string{"start", "stop", "update", "restart", "delete"} {
		t.Run(action, func(t *testing.T) {
			f := newNetworkFixture(t)
			created := f.createService(t, dockerServiceBody)
			original := f.api.data
			f.api.SetHostCommands(false)
			f.api.data = &serviceLeaseIntercept{DataStore: original, ServiceSnapshotStore: original.(store.ServiceSnapshotStore), before: func() {
				record, _ := original.GetService(context.Background(), created.ID)
				record.Driver, record.Config = "command", json.RawMessage(`{"start":["true"],"stop":["true"]}`)
				if err := original.UpdateService(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}}
			method, path, body := http.MethodPost, "/api/v1/services/"+created.ID+"/"+action, ""
			if action == "update" || action == "restart" {
				method, path, body = http.MethodPut, "/api/v1/services/"+created.ID, dockerServiceBody
			}
			if action == "restart" {
				path += "?restart=true"
			}
			if action == "delete" {
				method, path = http.MethodDelete, "/api/v1/services/"+created.ID
			}
			code, response := statusAndBody(t, f.srv, method, path, body)
			if code != http.StatusConflict {
				t.Fatalf("stale action: %d %s", code, response)
			}
			if len(f.svc.ensured) != 0 || len(f.svc.stopped) != 0 {
				t.Fatalf("stale action affected runtime: starts=%v stops=%v", f.svc.ensured, f.svc.stopped)
			}
			record, _ := original.GetService(context.Background(), created.ID)
			if record.Driver != "command" {
				t.Fatal("stale authorized write overwrote current host config")
			}
		})
	}
}

func TestShouldAuthorizeTransitiveHostRequirementsOnEveryServiceAction(t *testing.T) {
	f := newNetworkFixture(t)
	base := f.createService(t, `{"name":"host","driver":"command","config":{"start":["true"],"stop":["true"]}}`)
	dependent := f.createService(t, `{"name":"dependent","driver":"docker","config":{"containers":["dependent"]},"requires":["`+base.ID+`"]}`)
	f.api.SetHostCommands(false)
	for _, action := range []string{"start", "stop"} {
		if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/"+dependent.ID+"/"+action, ""); code != http.StatusForbidden {
			t.Fatalf("transitive host action=%s: %d %s", action, code, body)
		}
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPut, "/api/v1/services/"+dependent.ID+"?restart=true", `{"name":"dependent","driver":"docker","config":{"containers":["replacement"]}}`); code != http.StatusForbidden {
		t.Fatalf("removing a host requirement bypassed old authorization: %d %s", code, body)
	}
}

func TestShouldRejectStaleTransitiveServiceAuthorization(t *testing.T) {
	f := newNetworkFixture(t)
	base := f.createService(t, `{"name":"base","driver":"docker","config":{"containers":["base"]}}`)
	dependent := f.createService(t, `{"name":"dependent","driver":"docker","config":{"containers":["dependent"]},"requires":["`+base.ID+`"]}`)
	original := f.api.data
	f.api.SetHostCommands(false)
	f.api.data = &serviceLeaseIntercept{DataStore: original, ServiceSnapshotStore: original.(store.ServiceSnapshotStore), before: func() {
		record, _ := original.GetService(context.Background(), base.ID)
		record.Driver, record.Config = "command", json.RawMessage(`{"start":["true"],"stop":["true"]}`)
		if err := original.UpdateService(context.Background(), record); err != nil {
			t.Error(err)
		}
	}}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/"+dependent.ID+"/start", ""); code != http.StatusConflict {
		t.Fatalf("stale transitive start: %d %s", code, body)
	}
	if len(f.svc.ensured) != 0 || len(f.svc.stopped) != 0 {
		t.Fatal("stale transitive permission reached runtime")
	}
}

func TestShouldRejectConcurrentDeleteReferencesBeforeStopping(t *testing.T) {
	for _, reference := range []string{"endpoint", "dependent"} {
		t.Run(reference, func(t *testing.T) {
			f := newNetworkFixture(t)
			created := f.createService(t, dockerServiceBody)
			original := f.api.data
			f.api.data = &serviceLeaseIntercept{DataStore: original, ServiceSnapshotStore: original.(store.ServiceSnapshotStore), before: func() {
				var err error
				if reference == "endpoint" {
					err = original.CreateEndpoint(context.Background(), &store.Endpoint{ID: "late", AgentID: proxy.AgentID, OrgID: defaultOrgID, Kind: store.KindHTTP, Method: "proxy", Policy: &store.EndpointPolicy{ServiceID: created.ID, ProxyTargets: []string{"93.184.216.34:80"}}})
				} else {
					err = original.CreateService(context.Background(), &store.Service{ID: "late", OrgID: defaultOrgID, Name: "late", Driver: "docker", Config: json.RawMessage(`{"containers":["late"]}`), Requires: []string{created.ID}})
				}
				if err != nil {
					t.Error(err)
				}
			}}
			code, body := statusAndBody(t, f.srv, http.MethodDelete, "/api/v1/services/"+created.ID, "")
			if code != http.StatusConflict || len(f.svc.stopped) != 0 {
				t.Fatalf("late reference delete: %d %s stops=%v", code, body, f.svc.stopped)
			}
			if _, err := original.GetService(context.Background(), created.ID); err != nil {
				t.Fatal("referenced service was deleted")
			}
		})
	}
}

func TestShouldReturnTypedStopConflictAndRedactStatusFailures(t *testing.T) {
	f := newNetworkFixture(t)
	created := f.createService(t, dockerServiceBody)
	f.svc.stopErr = &services.RequirementInUseError{Dependents: []services.Status{{ID: "dependent", Name: "guacamole", State: services.StateStarting}}}
	code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services/"+created.ID+"/stop", "")
	if code != http.StatusConflict || !strings.Contains(body, "guacamole (starting)") || !strings.Contains(body, `"dependents":[`) || !strings.Contains(body, `"id":"dependent"`) {
		t.Fatalf("typed stop conflict: %d %s", code, body)
	}
	f.svc.mu.Lock()
	status := f.svc.statuses[created.ID]
	status.LastError = "command failed: private-environment-sentinel"
	f.svc.statuses[created.ID] = status
	f.svc.mu.Unlock()
	if body := statusBody(t, f, http.MethodGet, "/api/v1/services/"+created.ID); strings.Contains(body, "private-environment-sentinel") {
		t.Fatal("runtime diagnostics leaked a sensitive environment value")
	}
}
