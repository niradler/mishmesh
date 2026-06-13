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
