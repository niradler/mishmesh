package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
)

type thirdAccessProvider struct{ thirdConnectionProvider }

func (thirdAccessProvider) Describe() connections.Descriptor {
	return connections.Descriptor{Type: "edge-network", Label: "Existing edge network", Role: "access", Scope: "private", Protocols: []string{store.KindUDP}}
}

func (thirdAccessProvider) AccessHostname(domain, requested string) (string, error) {
	if requested == "" {
		requested = strings.SplitN(domain, ".", 2)[0]
	}
	requested = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(requested)), ".")
	if !strings.HasPrefix(requested, "edge-") {
		requested = "edge-" + requested
	}
	return connections.NormalizeAccessHostname(requested)
}

func (f *networkFixture) accessConnection(t *testing.T, kind, config, secret string) string {
	t.Helper()
	return createTestConnection(t, func(method, path, body string) (int, string) { return statusAndBody(t, f.srv, method, path, body) }, connectionCase{kind: kind, config: config, secrets: secret}, "access-"+store.NewID("test"))
}

func TestShouldDefaultAndNormalizeProviderOwnedAccessNames(t *testing.T) {
	f := newNetworkFixture(t)
	public := f.accessConnection(t, "cloudflare", `{}`, `{"tunnel_token":"`+testTunnelToken("acct1", "tun1")+`"}`)
	private := f.accessConnection(t, "tailscale", `{"tailnet":"example.com"}`, `{}`)
	created := f.createEndpoint(t, `{"method":"proxy","domain":"Notes.Example.Org.","policy":{"proxy_targets":["93.184.216.34:80"],"access":[{"connection_id":"`+public+`"},{"connection_id":"`+private+`"}]}}`)
	if len(created.Policy.Access) != 2 || created.Policy.Access[0].Hostname != "notes.example.org" || created.Policy.Access[1].Hostname != "notes" {
		t.Fatalf("provider defaults: %+v", created.Policy.Access)
	}
	code, body := statusAndBody(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+created.ID, `{"policy":{"proxy_targets":["93.184.216.34:80"],"access":[{"connection_id":"`+public+`","hostname":"Alternate.Example.Org."},{"connection_id":"`+private+`","hostname":"Other."}]}}`)
	if code != http.StatusOK || !strings.Contains(body, "alternate.example.org") || !strings.Contains(body, `"hostname":"other"`) {
		t.Fatalf("normalized override: %d %s", code, body)
	}
}

func TestShouldDispatchThirdProviderNamingAndProtocolWithoutTypeSwitches(t *testing.T) {
	f := newNetworkFixture(t)
	if err := f.api.connectionRegistry.Register(thirdAccessProvider{}); err != nil {
		t.Fatal(err)
	}
	id := f.accessConnection(t, "edge-network", `{}`, `{}`)
	if err := f.api.data.CreateAgent(context.Background(), &store.Agent{ID: "udp-agent", OrgID: defaultOrgID, Name: "udp-agent", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	created := f.createEndpoint(t, `{"agent_id":"udp-agent","kind":"udp","domain":"notes.example.org","policy":{"access":[{"connection_id":"`+id+`"}],"connect":{"name":"dns","allow_agents":["udp-agent"]}}}`)
	if created.Kind != store.KindUDP || created.Policy.Access[0].Hostname != "edge-notes" || created.Policy.Connect == nil {
		t.Fatalf("third provider/UDP: %+v", created)
	}
	code, body := statusAndBody(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+created.ID, `{"policy":{"access":[{"connection_id":"`+id+`"}],"request_headers_add":{"X-Test":"preserved"}}}`)
	if code != http.StatusOK {
		t.Fatalf("third stable edit: %d %s", code, body)
	}
	record, _ := f.api.data.GetEndpoint(context.Background(), created.ID)
	if record.Kind != store.KindUDP || record.Policy.Connect == nil || record.Policy.Connect.Name != "dns" || len(record.Policy.Connect.AllowAgents) != 1 {
		t.Fatalf("Connect/UDP lost: %+v", record)
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","domain":"http.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"access":[{"connection_id":"`+id+`"}]}}`); code != http.StatusBadRequest {
		t.Fatalf("unsupported third HTTP accepted: %d %s", code, body)
	}
}

func TestShouldRejectUnsupportedAccessProtocolsForeignRuntimeAndDuplicateRefs(t *testing.T) {
	f := newNetworkFixture(t)
	public := f.accessConnection(t, "cloudflare", `{}`, `{"tunnel_token":"`+testTunnelToken("acct1", "tun1")+`"}`)
	private := f.accessConnection(t, "tailscale", `{"tailnet":"example.com"}`, `{}`)
	runtime := f.accessConnection(t, "third", `{}`, `{}`)
	secondPublic := f.accessConnection(t, "cloudflare", `{}`, `{"tunnel_token":"`+testTunnelToken("acct1", "tun2")+`"}`)
	ctx := context.Background()
	if err := f.api.data.CreateOrg(ctx, &store.Org{ID: "foreign-access-org", Name: "foreign-access-org"}); err != nil {
		t.Fatal(err)
	}
	if err := f.api.data.CreateConnection(ctx, &store.Connection{ID: "foreign-access", OrgID: "foreign-access-org", Type: "cloudflare", Name: "private-connection-name", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{store.KindTCP, store.KindUDP, store.KindTLS} {
		for _, id := range []string{public, private} {
			body := `{"method":"proxy","kind":"` + kind + `","domain":"unsupported.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"access":[{"connection_id":"` + id + `"}]}}`
			if code, response := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/endpoints", body); code != http.StatusBadRequest {
				t.Fatalf("unsupported %s: %d %s", kind, code, response)
			}
		}
	}
	for _, refs := range []string{`[{"connection_id":"missing"}]`, `[{"connection_id":"foreign-access"}]`, `[{"connection_id":"` + runtime + `"}]`, `[{"connection_id":"` + public + `"},{"connection_id":"` + public + `","hostname":"other.example.org"}]`, `[{"connection_id":"` + public + `","hostname":"notes.example.org"},{"connection_id":"` + secondPublic + `","hostname":"NOTES.EXAMPLE.ORG."}]`} {
		if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","domain":"notes.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"access":`+refs+`}}`); code != http.StatusBadRequest || strings.Contains(body, "private-connection-name") {
			t.Fatalf("invalid access accepted/disclosed: %d %s", code, body)
		}
	}
}

func TestShouldPermitUnchangedAccessWhileInvalidConnectionRejectsChangedHostname(t *testing.T) {
	f := newNetworkFixture(t)
	id := f.accessConnection(t, "tailscale", `{"tailnet":"example.com"}`, `{}`)
	created := f.createEndpoint(t, `{"method":"proxy","domain":"notes.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"access":[{"connection_id":"`+id+`"}]}}`)
	record, _ := f.api.data.GetConnection(context.Background(), id)
	record.Config = json.RawMessage(`{"invalid":true}`)
	if err := f.api.data.UpdateConnection(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	for _, hostname := range []string{"notes", "NOTES.", "changed"} {
		code, body := statusAndBody(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+created.ID, `{"policy":{"proxy_targets":["93.184.216.35:80"],"access":[{"connection_id":"`+id+`","hostname":"`+hostname+`"}]}}`)
		want := http.StatusOK
		if hostname == "changed" {
			want = http.StatusBadRequest
		}
		if code != want {
			t.Fatalf("unchanged/change %s: %d %s", hostname, code, body)
		}
	}
}

type racingAccessData struct {
	store.DataStore
	connectionID string
}

func (d racingAccessData) CreateEndpoint(ctx context.Context, endpoint *store.Endpoint) error {
	record, err := d.DataStore.GetConnection(ctx, d.connectionID)
	if err != nil {
		return err
	}
	record.Type = "third"
	record.Config = json.RawMessage(`{}`)
	if err := d.DataStore.UpdateConnection(ctx, record); err != nil {
		return err
	}
	return d.DataStore.CreateEndpoint(ctx, endpoint)
}

func TestShouldFenceAccessRoleSnapshotBeforeEndpointCommit(t *testing.T) {
	f := newNetworkFixture(t)
	id := f.accessConnection(t, "tailscale", `{"tailnet":"example.com"}`, `{}`)
	f.api.data = racingAccessData{DataStore: f.api.data, connectionID: id}
	code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/endpoints", `{"method":"proxy","domain":"notes.example.org","policy":{"proxy_targets":["93.184.216.34:80"],"access":[{"connection_id":"`+id+`"}]}}`)
	if code != http.StatusConflict {
		t.Fatalf("stale access role saved: %d %s", code, body)
	}
	endpoints, _ := f.api.data.ListEndpointsByOrg(context.Background(), defaultOrgID)
	if len(endpoints) != 0 {
		t.Fatal("failed access snapshot partially saved endpoint")
	}
}

func TestShouldExposeAndUseProviderOwnedDetectionFields(t *testing.T) {
	f := newNetworkFixture(t)
	for _, input := range []string{`{"type":"cloudflare","input":{"metrics_addr":"127.0.0.1:20241"}}`, `{"type":"tailscale","input":{"socket":""}}`} {
		if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/connections/detect", input); code != http.StatusOK {
			t.Fatalf("descriptor detect: %d %s", code, body)
		}
	}
	if code, body := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/connections/detect", `{"type":"tailscale","input":{"socket":"","level":"managed"}}`); code != http.StatusBadRequest {
		t.Fatalf("full configuration accepted as Detect input: %d %s", code, body)
	}
	if body := statusBody(t, f, http.MethodGet, "/api/v1/connection-types"); !strings.Contains(body, `"detect_fields"`) || !strings.Contains(body, `"protocols":["http"]`) {
		t.Fatalf("missing provider capabilities: %s", body)
	}
}
