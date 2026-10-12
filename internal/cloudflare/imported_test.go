package cloudflare

import (
	"context"
	"strings"
	"testing"
)

func importedConfig(f *fakeCF) *Config {
	cfg := f.testClientConfig()
	cfg.APIToken = ""
	return cfg
}

func TestImportedSyncClassifiesHostnames(t *testing.T) {
	f := newFakeCF(t)
	f.local.setIngress(`[` +
		`{"hostname":"published.example.com","path":null,"service":"HTTP://127.0.0.1:8080/"},` +
		`{"hostname":"elsewhere.example.com","service":"http://10.0.0.5:80"},` +
		`{"hostname":"spare.example.com","service":"http://127.0.0.1:8080"},` +
		`{"hostname":"direct.example.com","service":"http://127.0.0.1:9000"},` +
		`{"hostname":"api.example.com","path":"/v1","service":"http://127.0.0.1:8080"},` +
		`{"service":"http_status:404"}]`)
	p, _ := newTestPublisher(t, f, importedConfig(f),
		Hostname{Host: "published.example.com", EndpointID: "ep1"},
		Hostname{Host: "elsewhere.example.com", EndpointID: "ep2"},
		Hostname{Host: "missing.example.com", EndpointID: "ep3"})
	st, err := p.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if st.Level != LevelImported || st.Tunnel == nil || st.Tunnel.Status != TunnelHealthy || st.Tunnel.Connections != 2 || st.Tunnel.Name != "lab" {
		t.Fatalf("status = %+v tunnel=%+v", st, st.Tunnel)
	}
	if !st.Connector.Reachable || st.Connector.ConnectorID != "conn-1" || len(st.Connector.EdgeLocations) != 2 {
		t.Fatalf("connector = %+v", st.Connector)
	}

	tests := []struct {
		host     string
		state    string
		endpoint string
		ingress  bool
		wantFix  string
		wantInfo string
	}{
		{"published.example.com", StatePublished, "ep1", true, "", ""},
		{"elsewhere.example.com", StateConflict, "ep2", true, "edit elsewhere.example.com", "10.0.0.5"},
		{"missing.example.com", StatePending, "ep3", false, "add missing.example.com", "no public hostname"},
		{"spare.example.com", StateUnclaimed, "", true, "", "no endpoint claims"},
		{"direct.example.com", StateUnclaimed, "", true, "", "not through mishmesh"},
	}
	for _, tt := range tests {
		h := hostEntryFor(t, st, tt.host)
		if h.State != tt.state || h.EndpointID != tt.endpoint || h.Ingress != tt.ingress || h.DNS != DNSUnchecked || h.Access != AccessNA {
			t.Errorf("%s = %+v", tt.host, h)
		}
		if tt.wantFix != "" && (!strings.Contains(h.Fix, tt.wantFix) || !strings.Contains(h.Fix, testOrigin) || !strings.Contains(h.Fix, "lab")) {
			t.Errorf("%s fix = %q", tt.host, h.Fix)
		}
		if tt.wantFix == "" && h.Fix != "" {
			t.Errorf("%s unexpected fix %q", tt.host, h.Fix)
		}
		if !strings.Contains(h.Detail, tt.wantInfo) {
			t.Errorf("%s detail = %q", tt.host, h.Detail)
		}
	}
	if len(st.Hostnames) != 5 {
		t.Fatalf("path rules and catch-all must be skipped: %+v", st.Hostnames)
	}
	if f.configPuts != 0 || f.dnsPosts != 0 || tunnelGets(f) != 0 {
		t.Fatal("imported level must never call the Cloudflare API")
	}
}

func TestImportedSyncConnectorUnreachable(t *testing.T) {
	f := newFakeCF(t)
	f.local.mu.Lock()
	f.local.down = true
	f.local.mu.Unlock()
	p, _ := newTestPublisher(t, f, importedConfig(f), Hostname{Host: "a.example.com", EndpointID: "ep1"})
	st, err := p.Sync(context.Background())
	if err == nil || st.LastError == "" {
		t.Fatalf("want error, got %v / %+v", err, st)
	}
	if st.Connector.Reachable || st.Tunnel.Status != TunnelUnknown {
		t.Fatalf("connector = %+v tunnel = %+v", st.Connector, st.Tunnel)
	}
	if h := hostEntryFor(t, st, "a.example.com"); h.State != StateError {
		t.Fatalf("host = %+v", h)
	}
}

func TestImportedSyncNoReadyConnections(t *testing.T) {
	f := newFakeCF(t)
	f.local.mu.Lock()
	f.local.ready = 0
	f.local.mu.Unlock()
	p, _ := newTestPublisher(t, f, importedConfig(f))
	st, _ := p.Sync(context.Background())
	if st.Tunnel.Status != TunnelDown || !st.Connector.Reachable {
		t.Fatalf("tunnel = %+v connector = %+v", st.Tunnel, st.Connector)
	}
}

func TestNewPublisherRejectsRemoteMetrics(t *testing.T) {
	f := newFakeCF(t)
	cfg := importedConfig(f)
	cfg.MetricsAddr = "10.0.0.1:20241"
	if _, err := NewPublisher(cfg, &staticSource{}, nil); err == nil {
		t.Fatal("remote metrics address accepted")
	}
}

func TestDetect(t *testing.T) {
	f := newFakeCF(t)
	f.local.setIngress(`[{"hostname":"B.example.com","service":"http://x"},{"hostname":"a.example.com","service":"http://x"},{"hostname":"a.example.com","path":"/x","service":"http://y"},{"service":"http_status:404"}]`)
	res, err := Detect(context.Background(), f.local.addr(), encodeToken(t, `{"a":"acct1","t":"tun1","s":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.AccountID != "acct1" || res.TunnelID != "tun1" || !res.Connector.Reachable {
		t.Fatalf("res = %+v", res)
	}
	if strings.Join(res.Hostnames, ",") != "a.example.com,b.example.com" {
		t.Fatalf("hostnames = %v", res.Hostnames)
	}
	if _, err := Detect(context.Background(), "8.8.8.8:53", ""); err == nil {
		t.Fatal("detect accepted a remote address")
	}
	if _, err := Detect(context.Background(), f.local.addr(), "bogus"); err == nil {
		t.Fatal("detect accepted a bad token")
	}
}

func TestUnclaimedHostnameMatchesPrivateEndpoint(t *testing.T) {
	f := newFakeCF(t)
	f.local.setIngress(`[` +
		`{"hostname":"portainer.example.com","service":"http://127.0.0.1:8080"},` +
		`{"hostname":"spare.example.com","service":"http://127.0.0.1:8080"},` +
		`{"service":"http_status:404"}]`)
	p, src := newTestPublisher(t, f, importedConfig(f))
	src.private = []Hostname{{Host: "portainer.example.com", EndpointID: "ep9"}}
	st, err := p.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	h := hostEntryFor(t, st, "portainer.example.com")
	if h.State != StateUnclaimed || h.EndpointID != "ep9" || h.Detail != unclaimedOff || h.Fix != unclaimedFix {
		t.Fatalf("private endpoint hostname = %+v", h)
	}
	spare := hostEntryFor(t, st, "spare.example.com")
	if spare.EndpointID != "" || spare.Detail != unclaimedOwned || spare.Fix != "" {
		t.Fatalf("spare hostname = %+v", spare)
	}
}

func TestPruneKeepsHostnamesServedByPrivateEndpoints(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, `{"ingress":[`+
		`{"hostname":"portainer.example.com","service":"http://127.0.0.1:8080"},`+
		`{"hostname":"gone.example.com","service":"http://127.0.0.1:8080"},`+
		`{"service":"http_status:404"}]}`)
	cfg := f.testClientConfig()
	cfg.Prune = true
	p, src := newTestPublisher(t, f, cfg)
	src.private = []Hostname{{Host: "portainer.example.com", EndpointID: "ep9"}}
	st, err := p.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	want := []string{"portainer.example.com|http://127.0.0.1:8080", "|http_status:404"}
	if got := f.ingressServices(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ingress = %v, want %v", got, want)
	}
	h := hostEntryFor(t, st, "portainer.example.com")
	if !h.Ingress || h.EndpointID != "ep9" || h.Fix != unclaimedFix {
		t.Fatalf("protected hostname = %+v", h)
	}
}
