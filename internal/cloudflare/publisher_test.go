package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSyncIngressOrdering(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   []string
	}{
		{
			name:   "no configuration yet",
			config: "",
			want:   []string{"a.example.com|" + testOrigin, "b.example.com|" + testOrigin, "|http_status:404"},
		},
		{
			name:   "foreign rules keep order and catch-all stays last",
			config: `{"ingress":[{"hostname":"x.example.com","service":"http://10.0.0.1:80"},{"hostname":"y.example.com","service":"http://10.0.0.2:80"},{"service":"http_status:404"}]}`,
			want: []string{
				"x.example.com|http://10.0.0.1:80", "y.example.com|http://10.0.0.2:80",
				"a.example.com|" + testOrigin, "b.example.com|" + testOrigin, "|http_status:404",
			},
		},
		{
			name:   "missing catch-all is appended",
			config: `{"ingress":[{"hostname":"x.example.com","service":"http://10.0.0.1:80"}]}`,
			want:   []string{"x.example.com|http://10.0.0.1:80", "a.example.com|" + testOrigin, "b.example.com|" + testOrigin, "|http_status:404"},
		},
		{
			name:   "existing owned rule with normalized origin is kept",
			config: `{"ingress":[{"hostname":"a.example.com","service":"HTTP://127.0.0.1:8080/"},{"service":"http_status:503"}]}`,
			want:   []string{"a.example.com|HTTP://127.0.0.1:8080/", "b.example.com|" + testOrigin, "|http_status:503"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeCF(t)
			if tt.config != "" {
				f.setConfig(t, tt.config)
			}
			fd, _ := newTestPublisher(t, f, f.testClientConfig(),
				Hostname{Host: "b.example.com", EndpointID: "ep2"}, Hostname{Host: "a.example.com", EndpointID: "ep1"})
			if _, err := fd.Sync(context.Background()); err != nil {
				t.Fatalf("sync: %v", err)
			}
			if got := f.ingressServices(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ingress = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSyncPreservesUnknownFields(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, `{"warp-routing":{"enabled":true},"originRequest":{"connectTimeout":"30s"},"ingress":[`+
		`{"hostname":"x.example.com","service":"http://10.0.0.1:80","originRequest":{"noTLSVerify":true},"futureField":[1,2,{"k":"v"}]},`+
		`{"service":"http_status:404","extra":"keep"}]}`)
	fd, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "a.example.com"})
	if _, err := fd.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	cfg := f.lastConfigPut
	if !reflect.DeepEqual(cfg["warp-routing"], map[string]any{"enabled": true}) {
		t.Fatalf("warp-routing dropped: %v", cfg)
	}
	if !reflect.DeepEqual(cfg["originRequest"], map[string]any{"connectTimeout": "30s"}) {
		t.Fatalf("originRequest dropped: %v", cfg)
	}
	rules := cfg["ingress"].([]any)
	first := rules[0].(map[string]any)
	if !reflect.DeepEqual(first["originRequest"], map[string]any{"noTLSVerify": true}) {
		t.Fatalf("rule originRequest dropped: %v", first)
	}
	if !reflect.DeepEqual(first["futureField"], []any{1.0, 2.0, map[string]any{"k": "v"}}) {
		t.Fatalf("rule unknown field dropped: %v", first)
	}
	last := rules[len(rules)-1].(map[string]any)
	if last["extra"] != "keep" {
		t.Fatalf("catch-all unknown field dropped: %v", last)
	}
}

func TestSyncNoopMakesNoWrites(t *testing.T) {
	f := newFakeCF(t)
	f.setApp(t, `{"id":"app1","self_hosted_domains":[],"destinations":[]}`)
	cfg := f.testClientConfig()
	cfg.AccessAppID = testAppID
	fd, _ := newTestPublisher(t, f, cfg, Hostname{Host: "a.example.com"}, Hostname{Host: "b.lab.example.com"})
	st, err := fd.Sync(context.Background())
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	for _, h := range st.Hostnames {
		if h.State != StatePublished {
			t.Fatalf("host %s state = %s detail=%s", h.Host, h.State, h.Detail)
		}
	}
	puts, posts, appPuts := f.configPuts, f.dnsPosts, f.appPuts
	if puts != 1 || posts != 2 || appPuts != 1 {
		t.Fatalf("first sync writes: puts=%d posts=%d appPuts=%d", puts, posts, appPuts)
	}
	st, err = fd.Sync(context.Background())
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if f.configPuts != puts || f.dnsPosts != posts || f.appPuts != appPuts {
		t.Fatalf("second sync wrote: puts=%d posts=%d appPuts=%d", f.configPuts, f.dnsPosts, f.appPuts)
	}
	if h := hostEntryFor(t, st, "b.lab.example.com"); h.State != StatePublished || !h.Ingress || h.DNS != DNSOK || h.Access != AccessOK {
		t.Fatalf("unexpected status: %+v", h)
	}
}

func TestSyncDNSZoneAndRecord(t *testing.T) {
	f := newFakeCF(t)
	fd, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "svc.lab.example.com"})
	if _, err := fd.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	recs := f.records["z-lab"]
	if len(recs) != 1 || recs[0].Type != "CNAME" || recs[0].Content != "tun1.cfargotunnel.com" || !recs[0].Proxied || recs[0].Comment != "managed by mishmesh" {
		t.Fatalf("records in lab zone = %+v", recs)
	}
	if len(f.records["z-example"]) != 0 {
		t.Fatalf("record created in wrong zone")
	}
}

func TestSyncNoZoneIsHostError(t *testing.T) {
	f := newFakeCF(t)
	fd, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "a.other.org"})
	st, _ := fd.Sync(context.Background())
	h := hostEntryFor(t, st, "a.other.org")
	if h.DNS != DNSError || h.State != StateError {
		t.Fatalf("status = %+v", h)
	}
}

func TestSyncDNSConflictUntouched(t *testing.T) {
	for _, prune := range []bool{false, true} {
		t.Run(map[bool]string{false: "prune off", true: "prune on"}[prune], func(t *testing.T) {
			f := newFakeCF(t)
			f.records["z-example"] = []DNSRecord{
				{ID: "r1", Type: "A", Name: "a.example.com", Content: "9.9.9.9"},
				{ID: "r2", Type: "CNAME", Name: "b.example.com", Content: "other.cfargotunnel.com"},
			}
			cfg := f.testClientConfig()
			cfg.Prune = prune
			fd, _ := newTestPublisher(t, f, cfg, Hostname{Host: "a.example.com"}, Hostname{Host: "b.example.com"})
			st, err := fd.Sync(context.Background())
			if err != nil {
				t.Fatalf("sync: %v", err)
			}
			for _, host := range []string{"a.example.com", "b.example.com"} {
				h := hostEntryFor(t, st, host)
				if h.DNS != DNSConflict || h.State != StateConflict || h.DNSDetail == "" {
					t.Fatalf("%s status = %+v", host, h)
				}
			}
			if f.dnsPosts != 0 || f.dnsDeletes != 0 || len(f.records["z-example"]) != 2 {
				t.Fatalf("conflicting records modified: posts=%d deletes=%d recs=%+v", f.dnsPosts, f.dnsDeletes, f.records)
			}
		})
	}
}

func TestSyncForeignIngressHostnameConflicts(t *testing.T) {
	f := newFakeCF(t)
	f.setConfig(t, `{"ingress":[{"hostname":"a.example.com","service":"http://10.0.0.1:80"},{"service":"http_status:404"}]}`)
	fd, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "a.example.com"})
	st, _ := fd.Sync(context.Background())
	h := hostEntryFor(t, st, "a.example.com")
	if h.State != StateConflict || !h.Ingress || h.Service != "http://10.0.0.1:80" || h.Fix == "" {
		t.Fatalf("status = %+v", h)
	}
	if f.configPuts != 0 || f.dnsPosts != 0 {
		t.Fatalf("wrote for conflicting hostname: puts=%d posts=%d", f.configPuts, f.dnsPosts)
	}
}

func TestSyncPrune(t *testing.T) {
	const initial = `{"ingress":[` +
		`{"hostname":"keep.example.com","service":"http://127.0.0.1:8080"},` +
		`{"hostname":"gone.example.com","service":"HTTP://127.0.0.1:8080/"},` +
		`{"hostname":"foreign.example.com","service":"http://10.0.0.1:80"},` +
		`{"service":"http_status:404"}]}`
	tests := []struct {
		name         string
		prune        bool
		wantServices []string
		wantRecords  int
		wantDeletes  int
		wantState    string
		wantAppHosts []string
	}{
		{
			name: "prune off reports orphan only",
			wantServices: []string{
				"keep.example.com|http://127.0.0.1:8080", "gone.example.com|HTTP://127.0.0.1:8080/",
				"foreign.example.com|http://10.0.0.1:80", "|http_status:404",
			},
			wantRecords:  2,
			wantState:    StateUnclaimed,
			wantAppHosts: []string{"keep.example.com", "gone.example.com"},
		},
		{
			name:         "prune on removes owned orphan only",
			prune:        true,
			wantServices: []string{"keep.example.com|http://127.0.0.1:8080", "foreign.example.com|http://10.0.0.1:80", "|http_status:404"},
			wantRecords:  1,
			wantDeletes:  1,
			wantState:    StateUnclaimed,
			wantAppHosts: []string{"keep.example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeCF(t)
			f.setConfig(t, initial)
			f.records["z-example"] = []DNSRecord{
				{ID: "k", Type: "CNAME", Name: "keep.example.com", Content: "tun1.cfargotunnel.com", Proxied: true},
				{ID: "g", Type: "CNAME", Name: "gone.example.com", Content: "tun1.cfargotunnel.com", Proxied: true},
				{ID: "f", Type: "CNAME", Name: "foreign.example.com", Content: "tun1.cfargotunnel.com", Proxied: true},
			}
			f.setApp(t, `{"self_hosted_domains":["keep.example.com","gone.example.com"],"destinations":[{"type":"public","uri":"keep.example.com"},{"type":"public","uri":"gone.example.com"}]}`)
			cfg := f.testClientConfig()
			cfg.Prune = tt.prune
			cfg.AccessAppID = testAppID
			fd, _ := newTestPublisher(t, f, cfg, Hostname{Host: "keep.example.com"})
			st, err := fd.Sync(context.Background())
			if err != nil {
				t.Fatalf("sync: %v", err)
			}
			if got := f.ingressServices(); !reflect.DeepEqual(got, tt.wantServices) {
				t.Fatalf("ingress = %v, want %v", got, tt.wantServices)
			}
			h := hostEntryFor(t, st, "gone.example.com")
			if h.State != tt.wantState || h.EndpointID != "" {
				t.Fatalf("orphan status = %+v", h)
			}
			if tt.prune && h.Ingress {
				t.Fatalf("pruned orphan still reports ingress: %+v", h)
			}
			if !tt.prune && !h.Ingress {
				t.Fatalf("unpruned orphan lost ingress: %+v", h)
			}
			if f.dnsDeletes != tt.wantDeletes {
				t.Fatalf("dns deletes = %d, want %d", f.dnsDeletes, tt.wantDeletes)
			}
			foreignKept := false
			for _, r := range f.records["z-example"] {
				if r.ID == "f" {
					foreignKept = true
				}
			}
			if !foreignKept || len(f.records["z-example"]) != tt.wantRecords+1 {
				t.Fatalf("records = %+v", f.records["z-example"])
			}
			if got := accessDomains(AccessApp(f.app)); !reflect.DeepEqual(got, tt.wantAppHosts) {
				t.Fatalf("access domains = %v, want %v", got, tt.wantAppHosts)
			}
			if len(st.Hostnames) != 3 {
				t.Fatalf("hostnames = %+v", st.Hostnames)
			}
			foreign := hostEntryFor(t, st, "foreign.example.com")
			if foreign.State != StateUnclaimed || foreign.Service != "http://10.0.0.1:80" || !strings.Contains(foreign.Detail, "not through mishmesh") {
				t.Fatalf("foreign = %+v", foreign)
			}
		})
	}
}

func TestSyncAccessAddPreservesFields(t *testing.T) {
	f := newFakeCF(t)
	f.setApp(t, `{"id":"app1","name":"lab","aud":"abc","session_duration":"24h","policies":[{"id":"p1","precedence":1}],"self_hosted_domains":["old.example.com"],"destinations":[{"type":"public","uri":"old.example.com"}],"big":12345678901234567890}`)
	cfg := f.testClientConfig()
	cfg.AccessAppID = testAppID
	fd, _ := newTestPublisher(t, f, cfg, Hostname{Host: "a.example.com"})
	if _, err := fd.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	app := f.app
	if !reflect.DeepEqual(app["self_hosted_domains"], []any{"old.example.com", "a.example.com"}) {
		t.Fatalf("domains = %v", app["self_hosted_domains"])
	}
	wantDest := []any{map[string]any{"type": "public", "uri": "old.example.com"}, map[string]any{"type": "public", "uri": "a.example.com"}}
	if !reflect.DeepEqual(app["destinations"], wantDest) {
		t.Fatalf("destinations = %v", app["destinations"])
	}
	if app["name"] != "lab" || app["aud"] != "abc" || app["session_duration"] != "24h" || app["policies"] == nil {
		t.Fatalf("unknown fields dropped: %v", app)
	}
	raw, _ := json.Marshal(app)
	if !strings.Contains(string(raw), `"id":"app1"`) {
		t.Fatalf("id dropped: %s", raw)
	}
}

func TestSyncAccessWithoutDestinations(t *testing.T) {
	f := newFakeCF(t)
	f.setApp(t, `{"self_hosted_domains":[]}`)
	cfg := f.testClientConfig()
	cfg.AccessAppID = testAppID
	fd, _ := newTestPublisher(t, f, cfg, Hostname{Host: "a.example.com"})
	if _, err := fd.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, ok := f.app["destinations"]; ok {
		t.Fatalf("destinations created although absent: %v", f.app)
	}
	if !reflect.DeepEqual(f.app["self_hosted_domains"], []any{"a.example.com"}) {
		t.Fatalf("domains = %v", f.app["self_hosted_domains"])
	}
}

func TestSyncPerHostErrorIsolation(t *testing.T) {
	f := newFakeCF(t)
	f.failPostHosts["bad.example.com"] = true
	fd, _ := newTestPublisher(t, f, f.testClientConfig(),
		Hostname{Host: "bad.example.com"}, Hostname{Host: "good.example.com"})
	st, _ := fd.Sync(context.Background())
	bad := hostEntryFor(t, st, "bad.example.com")
	good := hostEntryFor(t, st, "good.example.com")
	if bad.State != StateError || bad.DNS != DNSError {
		t.Fatalf("bad = %+v", bad)
	}
	if good.State != StatePublished {
		t.Fatalf("good = %+v", good)
	}
	if len(f.records["z-example"]) != 1 {
		t.Fatalf("records = %+v", f.records)
	}
}

func TestSyncRetries429(t *testing.T) {
	f := newFakeCF(t)
	f.limit429 = 2
	fd, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "a.example.com"})
	st, err := fd.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if hostEntryFor(t, st, "a.example.com").State != StatePublished {
		t.Fatalf("status = %+v", st.Hostnames)
	}
	if f.limit429 != 0 {
		t.Fatalf("429 responses not consumed: %d", f.limit429)
	}
}

func TestSyncGivesUpAfterRepeated429(t *testing.T) {
	f := newFakeCF(t)
	f.limit429 = 100
	fd, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "a.example.com"})
	st, err := fd.Sync(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("err = %v", err)
	}
	if st.LastError == "" || hostEntryFor(t, st, "a.example.com").State != StateError {
		t.Fatalf("status = %+v", st)
	}
}

func TestSyncSourceError(t *testing.T) {
	f := newFakeCF(t)
	fd, src := newTestPublisher(t, f, f.testClientConfig())
	src.err = errors.New("db down")
	st, err := fd.Sync(context.Background())
	if err == nil || !strings.Contains(st.LastError, "db down") {
		t.Fatalf("err=%v status=%+v", err, st)
	}
	if f.configPuts != 0 {
		t.Fatalf("wrote despite source error")
	}
}

func TestSyncStatusShape(t *testing.T) {
	f := newFakeCF(t)
	fd, _ := newTestPublisher(t, f, f.testClientConfig(), Hostname{Host: "a.example.com", EndpointID: "ep1"})
	st, err := fd.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"level", "tunnel", "origin", "connector", "last_sync", "hostnames"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing key %s in %s", key, raw)
		}
	}
	if m["level"] != LevelManaged {
		t.Fatalf("level = %v", m["level"])
	}
	if _, ok := m["supervisor"]; ok {
		t.Fatalf("supervisor present without config: %s", raw)
	}
	tun := m["tunnel"].(map[string]any)
	if tun["status"] != "healthy" || tun["name"] != "lab" || tun["connections"] != 1.0 {
		t.Fatalf("tunnel = %v", tun)
	}
	conn := m["connector"].(map[string]any)
	if conn["reachable"] != true || conn["ready_connections"] != 2.0 || conn["version"] != "2026.10.0" {
		t.Fatalf("connector = %v", conn)
	}
	host := m["hostnames"].([]any)[0].(map[string]any)
	for _, key := range []string{"host", "endpoint_id", "ingress", "dns", "access", "state"} {
		if _, ok := host[key]; !ok {
			t.Fatalf("hostname missing %s: %v", key, host)
		}
	}
	if host["access"] != AccessNA {
		t.Fatalf("access = %v", host["access"])
	}
}

func TestRunSyncsOnStartIntervalAndTrigger(t *testing.T) {
	f := newFakeCF(t)
	cfg := f.testClientConfig()
	cfg.SyncInterval = time.Hour
	fd, _ := newTestPublisher(t, f, cfg, Hostname{Host: "a.example.com"})
	fd.debounce = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		fd.Run(ctx)
	}()
	waitFor(t, func() bool { return tunnelGets(f) >= 1 })
	for i := 0; i < 5; i++ {
		fd.Trigger()
	}
	waitFor(t, func() bool { return tunnelGets(f) >= 2 })
	time.Sleep(100 * time.Millisecond)
	if got := tunnelGets(f); got != 2 {
		t.Fatalf("debounce should coalesce triggers into one sync, got %d syncs", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func tunnelGets(f *fakeCF) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tunnelGets
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestNormalizeService(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"HTTP://127.0.0.1:8080/", "http://127.0.0.1:8080"},
		{"http://Ingress.Local:80//", "http://ingress.local:80"},
		{"http_status:404", "http_status:404"},
	}
	for _, tt := range tests {
		if got := normalizeService(tt.in); got != tt.want {
			t.Errorf("normalizeService(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
