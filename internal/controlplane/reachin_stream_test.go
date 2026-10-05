package controlplane

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type streamAgent struct {
	id string

	mu       sync.Mutex
	gotKind  string
	gotMeta  map[string]string
	gotEP    string
	greeting string
	failWith string
}

func (s *streamAgent) AgentID() string { return s.id }
func (s *streamAgent) Close() error    { return nil }

func (s *streamAgent) OpenStream(_ context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	s.mu.Lock()
	s.gotEP, s.gotKind, s.gotMeta = endpointID, kind, meta
	s.mu.Unlock()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if s.failWith != "" {
			_ = tunnel.WriteStreamError(c, s.failWith)
			return
		}
		if s.greeting != "" {
			_, _ = io.WriteString(c, s.greeting)
		}
		data, _ := io.ReadAll(c)
		_, _ = io.WriteString(c, strings.ToUpper(string(data))+"|done")
		_ = c.(*net.TCPConn).CloseWrite()
	}()
	return net.Dial("tcp", ln.Addr().String())
}

type streamFixture struct {
	srv   *httptest.Server
	agent *streamAgent
	conns store.ConnectionStore
}

func newStreamFixture(t *testing.T, adminToken string, connected bool) *streamFixture {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	for _, id := range []string{"org_a", "org_b"} {
		if err := data.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.CreateAgent(ctx, &store.Agent{ID: "ag_a", OrgID: "org_a", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	conns := memory.NewConnStore()
	agent := &streamAgent{id: "ag_a"}
	if connected {
		conns.AddAgent(agent)
	}
	api := New(data, conns, adminToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetReachInEnabled(true)
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &streamFixture{srv: srv, agent: agent, conns: conns}
}

func (f *streamFixture) upgrade(t *testing.T, path, authHeader string, extra string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", f.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: api\r\nConnection: Upgrade\r\nUpgrade: mishmesh-stream\r\n%s%s\r\n", path, authHeader, extra)
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp, conn, br
}

func TestReachInStreamHalfCloseRoundTrip(t *testing.T) {
	f := newStreamFixture(t, "", true)
	f.agent.greeting = "hi|"

	resp, conn, br := f.upgrade(t, "/api/v1/reach/ag_a/stream?org_id=org_a&target=db.internal:5432&tls=true", "", "")
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "mishmesh-stream" {
		t.Fatalf("upgrade response: %d %v", resp.StatusCode, resp.Header)
	}

	if _, err := io.WriteString(conn, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi|HELLO|done" {
		t.Fatalf("reply after client half-close = %q", got)
	}

	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	if f.agent.gotKind != store.KindTCP || f.agent.gotEP != "" || f.agent.gotMeta["target"] != "db.internal:5432" || f.agent.gotMeta["tls"] != "true" {
		t.Fatalf("agent saw kind=%q ep=%q meta=%v", f.agent.gotKind, f.agent.gotEP, f.agent.gotMeta)
	}
}

func TestReachInStreamForwardsBytesSentWithUpgradeRequest(t *testing.T) {
	f := newStreamFixture(t, "", true)
	conn, err := net.Dial("tcp", f.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET /api/v1/reach/ag_a/stream?org_id=org_a&target=x:1 HTTP/1.1\r\nHost: api\r\nConnection: Upgrade\r\nUpgrade: mishmesh-stream\r\n\r\nearly"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(br)
	if string(got) != "EARLY|done" {
		t.Fatalf("early bytes must reach the agent target: %q", got)
	}
}

func TestReachInStreamRejections(t *testing.T) {
	tests := []struct {
		name      string
		admin     string
		connected bool
		path      string
		auth      string
		extra     string
		want      int
	}{
		{"other org gets 404", "", true, "/api/v1/reach/ag_a/stream?org_id=org_b&target=x:1", "", "", http.StatusNotFound},
		{"unknown agent gets 404", "", true, "/api/v1/reach/ag_zzz/stream?org_id=org_a&target=x:1", "", "", http.StatusNotFound},
		{"missing target is 400", "", true, "/api/v1/reach/ag_a/stream?org_id=org_a", "", "", http.StatusBadRequest},
		{"offline agent is 502", "", false, "/api/v1/reach/ag_a/stream?org_id=org_a&target=x:1", "", "", http.StatusBadGateway},
		{"no credentials is 401", "s3cret", true, "/api/v1/reach/ag_a/stream?org_id=org_a&target=x:1", "", "", http.StatusUnauthorized},
		{"wrong credentials is 401", "s3cret", true, "/api/v1/reach/ag_a/stream?org_id=org_a&target=x:1", "Authorization: Bearer nope\r\n", "", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStreamFixture(t, tt.admin, tt.connected)
			resp, _, _ := f.upgrade(t, tt.path, tt.auth, tt.extra)
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestReachInStreamRequiresUpgradeHeaders(t *testing.T) {
	f := newStreamFixture(t, "", true)
	resp, err := http.Get(f.srv.URL + "/api/v1/reach/ag_a/stream?org_id=org_a&target=x:1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain GET status = %d, want 400", resp.StatusCode)
	}
}

func TestReachInStreamAuthorizedWithBearer(t *testing.T) {
	f := newStreamFixture(t, "s3cret", true)
	resp, conn, br := f.upgrade(t, "/api/v1/reach/ag_a/stream?org_id=org_a&target=x:1", "Authorization: Bearer s3cret\r\n", "")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_, _ = io.WriteString(conn, "ping")
	_ = conn.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(br)
	if string(got) != "PING|done" {
		t.Fatalf("got %q", got)
	}
}

func TestReachInStreamMetersUsage(t *testing.T) {
	f := newStreamFixture(t, "", true)
	_, conn, br := f.upgrade(t, "/api/v1/reach/ag_a/stream?org_id=org_a&target=x:1", "", "")
	_, _ = io.WriteString(conn, "abcd")
	_ = conn.(*net.TCPConn).CloseWrite()
	_, _ = io.ReadAll(br)
	deadline := time.Now().Add(2 * time.Second)
	for f.conns.Usage("org_a") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.conns.Usage("org_a"); got != int64(len("abcd")+len("ABCD|done")) {
		t.Fatalf("usage = %d", got)
	}
}

func TestReachInHTTPAgentDialFailureIsBadGateway(t *testing.T) {
	f := newStreamFixture(t, "", true)
	f.agent.failWith = "connection refused"
	resp, err := http.Post(f.srv.URL+"/api/v1/reach/ag_a/http?org_id=org_a", "application/json", strings.NewReader(`{"target":"x:1","path":"/"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "connection refused") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}
