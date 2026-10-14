package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	testToken   = "test-token"
	testAccount = "acct1"
	testTunnel  = "tun1"
	testOrigin  = "http://127.0.0.1:8080"
	testAppID   = "app1"
)

type fakeCF struct {
	mu               sync.Mutex
	config           map[string]any
	configPuts       int
	lastConfigPut    map[string]any
	tunnelGets       int
	zones            []Zone
	records          map[string][]DNSRecord
	dnsPosts         int
	dnsDeletes       int
	failPostHosts    map[string]bool
	app              map[string]any
	appPuts          int
	limit429         int
	nextID           int
	server           *httptest.Server
	local            *fakeLocal
	normalizeIngress bool
	normalizeAccess  bool
}

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{
		zones:         []Zone{{ID: "z-example", Name: "example.com"}, {ID: "z-lab", Name: "lab.example.com"}},
		records:       map[string][]DNSRecord{},
		failPostHosts: map[string]bool{},
		local:         newFakeLocal(t),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts/{acct}/cfd_tunnel/{id}", f.handleTunnel)
	mux.HandleFunc("GET /accounts/{acct}/cfd_tunnel/{id}/configurations", f.handleGetConfig)
	mux.HandleFunc("PUT /accounts/{acct}/cfd_tunnel/{id}/configurations", f.handlePutConfig)
	mux.HandleFunc("GET /zones", f.handleZones)
	mux.HandleFunc("GET /zones/{zone}/dns_records", f.handleListRecords)
	mux.HandleFunc("POST /zones/{zone}/dns_records", f.handleCreateRecord)
	mux.HandleFunc("DELETE /zones/{zone}/dns_records/{id}", f.handleDeleteRecord)
	mux.HandleFunc("GET /accounts/{acct}/access/apps/{app}", f.handleGetApp)
	mux.HandleFunc("PUT /accounts/{acct}/access/apps/{app}", f.handlePutApp)
	f.server = httptest.NewUnstartedServer(f.wrap(mux))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.server.Listener = l
	f.server.Start()
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCF) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			reply(w, http.StatusForbidden, false, nil, 10000, "Authentication error")
			return
		}
		f.mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/configurations") && r.Method == http.MethodGet && f.limit429 > 0 {
			f.limit429--
			f.mu.Unlock()
			reply(w, http.StatusTooManyRequests, false, nil, 971, "rate limited")
			return
		}
		f.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func reply(w http.ResponseWriter, status int, ok bool, result any, code int, msg string) {
	body := map[string]any{"success": ok, "errors": []any{}, "result": result}
	if !ok {
		body["errors"] = []map[string]any{{"code": code, "message": msg}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeCF) handleTunnel(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.tunnelGets++
	f.mu.Unlock()
	reply(w, 200, true, map[string]any{
		"id": r.PathValue("id"), "name": "lab", "status": "healthy",
		"connections": []map[string]any{{"colo_name": "TLV", "id": "c1", "is_pending_reconnect": false, "origin_ip": "1.2.3.4", "opened_at": "2026-10-12T00:00:00Z", "client_version": "2026.9.0"}},
	}, 0, "")
}

func (f *fakeCF) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply(w, 200, true, map[string]any{"tunnel_id": r.PathValue("id"), "config": f.config}, 0, "")
}

func (f *fakeCF) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		reply(w, 400, false, nil, 1, err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configPuts++
	if f.normalizeIngress {
		for _, raw := range body.Config["ingress"].([]any) {
			if rule := raw.(map[string]any); rule["hostname"] != nil {
				rule["path"] = nil
				if _, exists := rule["originRequest"]; !exists {
					rule["originRequest"] = map[string]any{"connectTimeout": 30}
				}
			}
		}
	}
	f.config = body.Config
	f.lastConfigPut = body.Config
	reply(w, 200, true, map[string]any{"config": body.Config}, 0, "")
}

func (f *fakeCF) handleZones(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true, "errors": []any{}, "result": f.zones,
		"result_info": map[string]any{"page": 1, "total_pages": 1},
	})
}

func (f *fakeCF) handleListRecords(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := r.URL.Query().Get("name")
	out := []DNSRecord{}
	for _, rec := range f.records[r.PathValue("zone")] {
		if rec.Name == name {
			out = append(out, rec)
		}
	}
	reply(w, 200, true, out, 0, "")
}

func (f *fakeCF) handleCreateRecord(w http.ResponseWriter, r *http.Request) {
	var rec DNSRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		reply(w, 400, false, nil, 1, err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPostHosts[rec.Name] {
		reply(w, 500, false, nil, 9000, "boom")
		return
	}
	f.dnsPosts++
	f.nextID++
	rec.ID = fmt.Sprintf("rec%d", f.nextID)
	zone := r.PathValue("zone")
	f.records[zone] = append(f.records[zone], rec)
	reply(w, 200, true, rec, 0, "")
}

func (f *fakeCF) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	zone := r.PathValue("zone")
	kept := f.records[zone][:0:0]
	for _, rec := range f.records[zone] {
		if rec.ID != r.PathValue("id") {
			kept = append(kept, rec)
		}
	}
	f.records[zone] = kept
	f.dnsDeletes++
	reply(w, 200, true, map[string]any{"id": r.PathValue("id")}, 0, "")
}

func (f *fakeCF) handleGetApp(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply(w, 200, true, f.app, 0, "")
}

func (f *fakeCF) handlePutApp(w http.ResponseWriter, r *http.Request) {
	var app map[string]any
	if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
		reply(w, 400, false, nil, 1, err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appPuts++
	if f.normalizeAccess {
		for _, raw := range app["destinations"].([]any) {
			if destination := raw.(map[string]any); destination["port"] == nil {
				destination["port"] = 443
			}
		}
	}
	f.app = app
	reply(w, 200, true, app, 0, "")
}

func (f *fakeCF) setConfig(t *testing.T, raw string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad config json: %v", err)
	}
	f.config = m
}

func (f *fakeCF) setApp(t *testing.T, raw string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad app json: %v", err)
	}
	f.app = m
}

func (f *fakeCF) ingressServices() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	rules, _ := f.config["ingress"].([]any)
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		m := r.(map[string]any)
		h, _ := m["hostname"].(string)
		s, _ := m["service"].(string)
		out = append(out, h+"|"+s)
	}
	return out
}

func (f *fakeCF) testClientConfig() *Config {
	return &Config{
		OrgID:        "org_default",
		Name:         "lab",
		APIToken:     testToken,
		AccountID:    testAccount,
		TunnelID:     testTunnel,
		Origin:       testOrigin,
		MetricsAddr:  f.local.addr(),
		SyncInterval: DefaultSyncInterval,
		BaseURL:      f.server.URL,
	}
}

type staticSource struct {
	hosts   []Hostname
	private []Hostname
	err     error
}

func (s *staticSource) Hostnames(_ context.Context, _ string) ([]Hostname, error) {
	out := make([]Hostname, 0, len(s.hosts)+len(s.private))
	for _, h := range s.hosts {
		h.Public = true
		out = append(out, h)
	}
	return append(out, s.private...), s.err
}

func newTestPublisher(t *testing.T, f *fakeCF, cfg *Config, hosts ...Hostname) (*Publisher, *staticSource) {
	t.Helper()
	src := &staticSource{hosts: hosts}
	p, err := NewPublisher(cfg, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.client != nil {
		p.client.backoff = 0
	}
	return p, src
}

type fakeLocal struct {
	mu      sync.Mutex
	ingress string
	ready   int
	down    bool
	server  *httptest.Server
}

const testMetrics = `# HELP build_info Build and version information
build_info{goversion="go1.26",revision="x",type="",version="2026.10.0"} 1
cloudflared_tunnel_ha_connections 2
cloudflared_tunnel_server_locations{connection_id="0",edge_location="tlv01"} 1
cloudflared_tunnel_server_locations{connection_id="1",edge_location="fra07"} 0
cloudflared_tunnel_server_locations{connection_id="1",edge_location="fra19"} 1
`

func newFakeLocal(t *testing.T) *fakeLocal {
	t.Helper()
	l := &fakeLocal{ready: 2, ingress: `[{"service":"http_status:404"}]`}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		code := http.StatusOK
		if l.ready == 0 {
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"status":%d,"readyConnections":%d,"connectorId":"conn-1"}`, code, l.ready)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, testMetrics)
	})
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, _ *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		fmt.Fprintf(w, `{"version":3,"config":{"ingress":%s,"warp-routing":{"enabled":false}}}`, l.ingress)
	})
	l.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		down := l.down
		l.mu.Unlock()
		if down {
			hj, ok := w.(http.Hijacker)
			if ok {
				c, _, _ := hj.Hijack()
				c.Close()
				return
			}
		}
		mux.ServeHTTP(w, r)
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l.server.Listener = ln
	l.server.Start()
	t.Cleanup(l.server.Close)
	return l
}

func (l *fakeLocal) addr() string {
	return strings.TrimPrefix(l.server.URL, "http://")
}

func (l *fakeLocal) setIngress(raw string) {
	l.mu.Lock()
	l.ingress = raw
	l.mu.Unlock()
}

func hostEntryFor(t *testing.T, st Status, host string) HostStatus {
	t.Helper()
	for _, h := range st.Hostnames {
		if h.Host == host {
			return h
		}
	}
	t.Fatalf("host %s not in status: %+v", host, st.Hostnames)
	return HostStatus{}
}
