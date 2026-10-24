package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
)

const testCatchAll = `{"ingress":[{"hostname":"a.example.com","service":"http://127.0.0.1:8080"},{"service":"http_status:404"}]}`

func hostnameKind(err error) connections.HostnameErrorKind {
	var hostnameErr *connections.HostnameError
	if errors.As(err, &hostnameErr) {
		return hostnameErr.Kind
	}
	return ""
}

func TestShouldAddDirectHostnameBeforeCatchAllWithProxiedCNAME(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, testCatchAll)
	p, _ := newTestPublisher(t, f, f.testClientConfig())
	if err := p.AddHostname(context.Background(), "Vault.Example.com.", "http://localhost:8000"); err != nil {
		t.Fatal(err)
	}
	want := []string{"a.example.com|http://127.0.0.1:8080", "vault.example.com|http://localhost:8000", "|http_status:404"}
	if got := f.ingressServices(); !slices.Equal(got, want) {
		t.Fatalf("ingress = %v, want %v", got, want)
	}
	records := f.records["z-example"]
	if len(records) != 1 || records[0].Name != "vault.example.com" || records[0].Content != "tun1.cfargotunnel.com" || !records[0].Proxied || records[0].Comment != dnsComment {
		t.Fatalf("records = %+v", records)
	}
}

func TestShouldAppendCatchAllWhenTunnelHasNone(t *testing.T) {
	f := newFakeCF(t)
	p, _ := newTestPublisher(t, f, f.testClientConfig())
	if err := p.AddHostname(context.Background(), "ssh.lab.example.com", "ssh://localhost:22"); err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh.lab.example.com|ssh://localhost:22", "|http_status:404"}
	if got := f.ingressServices(); !slices.Equal(got, want) {
		t.Fatalf("ingress = %v, want %v", got, want)
	}
	if len(f.records["z-lab"]) != 1 {
		t.Fatalf("record not created in the most specific zone: %+v", f.records)
	}
}

func TestShouldReuseExistingTunnelRecordWhenAddingHostname(t *testing.T) {
	f := newFakeCF(t)
	f.records["z-example"] = []DNSRecord{{ID: "keep", Type: "CNAME", Name: "vault.example.com", Content: "tun1.cfargotunnel.com", Proxied: true}}
	p, _ := newTestPublisher(t, f, f.testClientConfig())
	if err := p.AddHostname(context.Background(), "vault.example.com", "http://localhost:8000"); err != nil {
		t.Fatal(err)
	}
	if f.dnsPosts != 0 || len(f.records["z-example"]) != 1 {
		t.Fatalf("existing tunnel record duplicated: posts=%d records=%+v", f.dnsPosts, f.records)
	}
}

func TestShouldRejectHostnameAdditionsThatWouldOverrideState(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		service string
		setup   func(*fakeCF)
		kind    connections.HostnameErrorKind
	}{
		{"already routed", "a.example.com", "http://localhost:9000", nil, connections.HostnameConflict},
		{"foreign record", "vault.example.com", "http://localhost:8000", func(f *fakeCF) {
			f.records["z-example"] = []DNSRecord{{ID: "r", Type: "A", Name: "vault.example.com", Content: "1.2.3.4"}}
		}, connections.HostnameConflict},
		{"origin service", "vault.example.com", testOrigin, nil, connections.HostnameInvalid},
		{"origin service with slash", "vault.example.com", testOrigin + "/", nil, connections.HostnameInvalid},
		{"unknown scheme", "vault.example.com", "ftp://localhost", nil, connections.HostnameInvalid},
		{"no host", "vault.example.com", "http://", nil, connections.HostnameInvalid},
		{"credentials", "vault.example.com", "http://user:pw@localhost", nil, connections.HostnameInvalid},
		{"query", "vault.example.com", "http://localhost?x=1", nil, connections.HostnameInvalid},
		{"path", "vault.example.com", "http://localhost:8000/app", nil, connections.HostnameInvalid},
		{"path rules", "vault.example.com", "http://localhost:8000", func(f *fakeCF) {
			f.setConfig(t, `{"ingress":[{"hostname":"vault.example.com","path":"/api","service":"http://localhost:9000"},{"service":"http_status:404"}]}`)
		}, connections.HostnameConflict},
		{"bad hostname", "not a host", "http://localhost:8000", nil, connections.HostnameInvalid},
		{"no zone", "vault.other.org", "http://localhost:8000", nil, connections.HostnameInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCF(t)
			f.setConfig(t, testCatchAll)
			if tc.setup != nil {
				tc.setup(f)
			}
			p, _ := newTestPublisher(t, f, f.testClientConfig())
			err := p.AddHostname(context.Background(), tc.host, tc.service)
			if got := hostnameKind(err); got != tc.kind {
				t.Fatalf("kind = %q (%v), want %q", got, err, tc.kind)
			}
			if f.configPuts != 0 || f.dnsPosts != 0 {
				t.Fatalf("rejected add changed state: puts=%d posts=%d", f.configPuts, f.dnsPosts)
			}
		})
	}
}

func TestShouldRollBackRouteWhenRecordCreationFails(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, testCatchAll)
	f.failPostHosts["vault.example.com"] = true
	p, _ := newTestPublisher(t, f, f.testClientConfig())
	err := p.AddHostname(context.Background(), "vault.example.com", "http://localhost:8000")
	if err == nil || hostnameKind(err) != "" {
		t.Fatalf("err = %v, want provider failure", err)
	}
	want := []string{"a.example.com|http://127.0.0.1:8080", "|http_status:404"}
	if got := f.ingressServices(); !slices.Equal(got, want) {
		t.Fatalf("route not rolled back: %v", got)
	}
}

func TestShouldRequireAPITokenToChangeHostnames(t *testing.T) {
	f := newFakeCF(t)
	cfg := f.testClientConfig()
	cfg.APIToken = ""
	p, _ := newTestPublisher(t, f, cfg)
	if kind := hostnameKind(p.AddHostname(context.Background(), "vault.example.com", "http://localhost:8000")); kind != connections.HostnameUnsupported {
		t.Fatalf("add kind = %q", kind)
	}
	if kind := hostnameKind(p.RemoveHostname(context.Background(), "vault.example.com")); kind != connections.HostnameUnsupported {
		t.Fatalf("remove kind = %q", kind)
	}
}

func TestShouldRemoveRouteAndTunnelRecordsButKeepForeignRecords(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, `{"ingress":[{"hostname":"vault.example.com","service":"http://localhost:8000"},{"hostname":"vault.example.com","path":"/admin","service":"http://localhost:8001"},{"hostname":"b.example.com","service":"http://localhost:9000"},{"service":"http_status:404"}]}`)
	f.records["z-example"] = []DNSRecord{
		{ID: "tunnel", Type: "CNAME", Name: "vault.example.com", Content: "tun1.cfargotunnel.com"},
		{ID: "txt", Type: "TXT", Name: "vault.example.com", Content: "verification"},
	}
	p, _ := newTestPublisher(t, f, f.testClientConfig())
	if err := p.RemoveHostname(context.Background(), "vault.example.com"); err != nil {
		t.Fatal(err)
	}
	want := []string{"b.example.com|http://localhost:9000", "|http_status:404"}
	if got := f.ingressServices(); !slices.Equal(got, want) {
		t.Fatalf("ingress = %v, want %v", got, want)
	}
	if records := f.records["z-example"]; len(records) != 1 || records[0].ID != "txt" {
		t.Fatalf("records = %+v", records)
	}
}

func TestShouldRemoveDanglingTunnelRecordWithoutRoute(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, testCatchAll)
	f.records["z-example"] = []DNSRecord{{ID: "tunnel", Type: "CNAME", Name: "old.example.com", Content: "tun1.cfargotunnel.com"}}
	p, _ := newTestPublisher(t, f, f.testClientConfig())
	if err := p.RemoveHostname(context.Background(), "old.example.com"); err != nil {
		t.Fatal(err)
	}
	if f.configPuts != 0 || len(f.records["z-example"]) != 0 {
		t.Fatalf("puts=%d records=%+v", f.configPuts, f.records)
	}
}

func TestShouldRefuseRemovingUnknownOrEndpointHostnames(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, testCatchAll)
	p, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "a.example.com", EndpointID: "ep1"})
	err := p.RemoveHostname(context.Background(), "a.example.com")
	var hostnameErr *connections.HostnameError
	if !errors.As(err, &hostnameErr) || hostnameErr.Kind != connections.HostnameInUse || !slices.Equal(hostnameErr.EndpointIDs, []string{"ep1"}) {
		t.Fatalf("err = %#v", err)
	}
	if kind := hostnameKind(p.RemoveHostname(context.Background(), "missing.example.com")); kind != connections.HostnameNotFound {
		t.Fatalf("missing kind = %q", kind)
	}
	if f.configPuts != 0 || f.dnsDeletes != 0 {
		t.Fatalf("refused removal changed state: puts=%d deletes=%d", f.configPuts, f.dnsDeletes)
	}
}

func TestShouldRefuseChangingHostnamesClaimedByAnotherConnection(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.data.endpoints = append(fixture.data.endpoints, &store.Endpoint{ID: "other", OrgID: "foreign", Domain: "x.example.com", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "b", Hostname: "orphan.example.com"}}}})
	instance, err := fixture.open.Open(fixture.config, connections.Deps{Data: fixture.data, Origin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	manager := instance.(connections.HostnameManager)
	if kind := hostnameKind(manager.RemoveHostname(context.Background(), "orphan.example.com")); kind != connections.HostnameConflict {
		t.Fatalf("remove kind = %q", kind)
	}
	fixture.data.endpoints[1].Policy.Access[0].Hostname = "new.example.com"
	if kind := hostnameKind(manager.AddHostname(context.Background(), "new.example.com", "http://localhost:8000")); kind != connections.HostnameConflict {
		t.Fatalf("add kind = %q", kind)
	}
	if fixture.fake.configPuts != 0 || fixture.fake.dnsDeletes != 0 || fixture.fake.dnsPosts != 0 {
		t.Fatalf("refused change touched Cloudflare: puts=%d deletes=%d posts=%d", fixture.fake.configPuts, fixture.fake.dnsDeletes, fixture.fake.dnsPosts)
	}
}

func TestShouldReleaseOwnedAccessWhenRemovingHostname(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.sync(t)
	fixture.data.endpoints[0].Policy.Access = nil
	record, err := fixture.data.GetConnection(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Record = *record
	instance, err := fixture.open.Open(config, connections.Deps{Data: fixture.data, Origin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.(connections.HostnameManager).RemoveHostname(context.Background(), "a.example.com"); err != nil {
		t.Fatal(err)
	}
	if accessHasHost(AccessApp(fixture.fake.app), "a.example.com") || !accessHasHost(AccessApp(fixture.fake.app), "orphan.example.com") {
		t.Fatalf("access app = %v", fixture.fake.app)
	}
	if strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "a.example.com|") {
		t.Fatalf("ingress = %v", fixture.fake.ingressServices())
	}
	stored, err := fixture.data.GetConnection(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	var state ownershipState
	if err := json.Unmarshal(stored.State, &state); err != nil {
		t.Fatal(err)
	}
	if _, owned := state.Routes["a.example.com"]; owned {
		t.Fatalf("ownership kept after removal: %+v", state.Routes)
	}
}
