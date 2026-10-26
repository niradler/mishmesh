package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func assertContains(t *testing.T, body, substr string) {
	t.Helper()
	if !strings.Contains(body, substr) {
		t.Errorf("expected %q in metrics output", substr)
	}
}

func TestMetricsAllMethodsAndScrape(t *testing.T) {
	m := New()

	m.AgentConnected()
	m.AgentConnected()
	m.AgentDisconnected()

	m.StreamOpened("http")
	m.StreamOpened("tcp")
	m.StreamClosed("http")

	m.AddBytes("http", 1024, 512)
	m.HandshakeFailure()
	m.HTTPRequest(200)
	m.HTTPRequest(404)
	m.HTTPRequest(503)

	body := scrape(t, m)

	assertContains(t, body, "mishmesh_agents_connected")
	assertContains(t, body, "mishmesh_streams_opened_total")
	assertContains(t, body, "mishmesh_streams_active")
	assertContains(t, body, "mishmesh_bytes_in_total")
	assertContains(t, body, "mishmesh_bytes_out_total")
	assertContains(t, body, "mishmesh_handshake_failures_total")
	assertContains(t, body, "mishmesh_http_requests_total")
	assertContains(t, body, `code="2xx"`)
	assertContains(t, body, `code="4xx"`)
	assertContains(t, body, `code="5xx"`)
}

func TestNilReceiverNoPanic(t *testing.T) {
	var m *Metrics
	m.AgentConnected()
	m.AgentDisconnected()
	m.StreamOpened("http")
	m.StreamClosed("http")
	m.AddBytes("http", 100, 200)
	m.HandshakeFailure()
	m.HTTPRequest(200)

	resp := httptest.NewRecorder()
	m.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/metrics", nil))
}

func TestRuntimeAndProcessCollectorsScrape(t *testing.T) {
	body := scrape(t, New())
	assertContains(t, body, "go_goroutines")
	assertContains(t, body, "go_memstats_alloc_bytes")
	assertContains(t, body, "process_open_fds")
}

type fakeAccessLogStats struct{ dropped, suppressed, failed int64 }

func (f fakeAccessLogStats) Dropped() int64       { return f.dropped }
func (f fakeAccessLogStats) Suppressed() int64    { return f.suppressed }
func (f fakeAccessLogStats) WriteFailures() int64 { return f.failed }

func TestAccessLogCountersScrape(t *testing.T) {
	m := New()
	m.RegisterAccessLog(fakeAccessLogStats{dropped: 7, suppressed: 3, failed: 2})
	body := scrape(t, m)
	assertContains(t, body, "mishmesh_access_log_dropped_total 7")
	assertContains(t, body, "mishmesh_access_log_suppressed_total 3")
	assertContains(t, body, "mishmesh_access_log_write_failures_total 2")
	var nilMetrics *Metrics
	nilMetrics.RegisterAccessLog(fakeAccessLogStats{})
}
