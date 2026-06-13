package ingress

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestCIDRMatch(t *testing.T) {
	cases := []struct {
		cidrs []string
		ip    string
		want  bool
	}{
		{[]string{"10.0.0.0/8"}, "10.1.2.3", true},
		{[]string{"10.0.0.0/8"}, "11.0.0.1", false},
		{[]string{"192.168.1.5"}, "192.168.1.5", true},
		{[]string{"192.168.1.5"}, "192.168.1.6", false},
		{[]string{"::1/128"}, "::1", true},
	}
	for _, c := range cases {
		got := cidrMatch(c.cidrs, net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("cidrMatch(%v,%q)=%v want %v", c.cidrs, c.ip, got, c.want)
		}
	}
}

func TestIsUpgrade(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if isUpgrade(r) {
		t.Fatal("plain request should not be upgrade")
	}
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "keep-alive, Upgrade")
	if !isUpgrade(r) {
		t.Fatal("websocket request should be upgrade")
	}
}

func TestBuildOutboundRequestPathRewrite(t *testing.T) {
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{StripPathPrefix: "/api", AddPathPrefix: "/v2", HostHeader: "internal.local"}}
	r := httptest.NewRequest("GET", "http://demo.localhost/api/users", nil)
	out := buildOutboundRequest(r, context.Background(), ep, "/api/users")
	if out.URL.Path != "/v2/users" {
		t.Fatalf("path = %q want /v2/users", out.URL.Path)
	}
	if out.Host != "internal.local" {
		t.Fatalf("host = %q want internal.local", out.Host)
	}
}

func TestApplyRequestResponsePolicy(t *testing.T) {
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{
		RequestHeadersAdd:     map[string]string{"X-From": "edge"},
		RequestHeadersRemove:  []string{"Cookie"},
		ResponseHeadersAdd:    map[string]string{"X-Served": "mishmesh"},
		ResponseHeadersRemove: []string{"Server"},
	}}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Cookie", "secret")
	applyRequestPolicy(req, ep)
	if req.Header.Get("X-From") != "edge" || req.Header.Get("Cookie") != "" {
		t.Fatalf("request policy not applied: %v", req.Header)
	}
	h := http.Header{"Server": {"nginx"}}
	applyResponsePolicy(h, ep)
	if h.Get("X-Served") != "mishmesh" || h.Get("Server") != "" {
		t.Fatalf("response policy not applied: %v", h)
	}
}

func TestBasicAuthGate(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{BasicAuthUser: "alice", BasicAuthHash: string(hash)}}

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if applyPolicyGate(w, r, ep) {
		t.Fatal("missing credentials should be blocked")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d want 401", w.Code)
	}

	r2 := httptest.NewRequest("GET", "/", nil)
	r2.SetBasicAuth("alice", "s3cret")
	if !applyPolicyGate(httptest.NewRecorder(), r2, ep) {
		t.Fatal("valid credentials should pass")
	}
}

func TestIPDenyGate(t *testing.T) {
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{IPDeny: []string{"203.0.113.0/24"}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.7:5000"
	w := httptest.NewRecorder()
	if applyPolicyGate(w, r, ep) || w.Code != http.StatusForbidden {
		t.Fatalf("denied IP should be 403, got %d", w.Code)
	}
}

func TestOIDCPolicyFailsClosed(t *testing.T) {
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: "x"}}}
	w := httptest.NewRecorder()
	if applyPolicyGate(w, httptest.NewRequest("GET", "/", nil), ep) {
		t.Fatal("oidc policy must fail closed, not pass")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d want 503", w.Code)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type nopConns struct{}

func (nopConns) AddAgent(store.AgentConn) store.AgentConn       { return nil }
func (nopConns) RemoveAgent(store.AgentConn)                    {}
func (nopConns) GetAgent(string) (store.AgentConn, bool)        { return nil, false }
func (nopConns) BindEndpoint(string, string)                    {}
func (nopConns) UnbindEndpoint(string)                          {}
func (nopConns) ResolveEndpoint(string) (store.AgentConn, bool) { return nil, false }
func (nopConns) AddUsage(string, int64)                         {}
func (nopConns) Usage(string) int64                             { return 0 }

type fakeAgentConn struct {
	backend http.Handler
}

func (f *fakeAgentConn) AgentID() string { return "ag_test" }
func (f *fakeAgentConn) Close() error    { return nil }
func (f *fakeAgentConn) OpenStream(_ context.Context, _, _ string, _ map[string]string) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		br := bufio.NewReader(server)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		rec := httptest.NewRecorder()
		f.backend.ServeHTTP(rec, req)
		_ = rec.Result().Write(server)
	}()
	return client, nil
}

func TestProxyHTTPWithCompressionAndHeaders(t *testing.T) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-From") != "edge" {
			t.Errorf("request header not rewritten: %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, strings.Repeat("hello ", 200))
	})
	ep := &store.Endpoint{ID: "ep_1", OrgID: "org_1", Kind: store.KindHTTP, Policy: &store.EndpointPolicy{
		RequestHeadersAdd: map[string]string{"X-From": "edge"},
		Compression:       true,
	}}
	ing := &Ingress{log: discardLogger(), conns: nopConns{}}
	r := httptest.NewRequest("GET", "http://demo.localhost/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	ing.proxyHTTP(w, r, &fakeAgentConn{backend: backend}, ep, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip encoding, headers: %v", w.Header())
	}
}
