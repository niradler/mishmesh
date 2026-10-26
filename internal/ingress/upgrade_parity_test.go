package ingress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type capturingUpgradeAgent struct {
	seen chan *http.Request
}

func (capturingUpgradeAgent) AgentID() string { return "ag_test" }
func (capturingUpgradeAgent) Close() error    { return nil }
func (a capturingUpgradeAgent) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		a.seen <- req
		_, _ = io.WriteString(server, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	}()
	return client, nil
}

func parityPolicyEndpoint() *store.Endpoint {
	return &store.Endpoint{ID: "ep_1", OrgID: "org_1", Kind: store.KindHTTP, Policy: &store.EndpointPolicy{
		HostHeader:           "origin.internal",
		RequestHeadersAdd:    map[string]string{"X-Added": "1"},
		RequestHeadersRemove: []string{"X-Remove"},
		StripPathPrefix:      "/api",
		AddPathPrefix:        "/v2",
	}}
}

func parityRequest(upgrade bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/api/items?q=1", nil)
	r.RemoteAddr = "203.0.113.9:4321"
	r.Header.Set("X-Remove", "secret")
	r.Header.Set("X-Forwarded-For", "6.6.6.6")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("Forwarded", "for=6.6.6.6")
	r.Header.Set("Cookie", "mm_oidc=a; mm_ep_session=b; app=c")
	if upgrade {
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
	}
	return r
}

func comparableHeaders(h http.Header) http.Header {
	out := h.Clone()
	for _, name := range []string{"Connection", "Upgrade", "Accept-Encoding", "User-Agent", "Content-Length"} {
		out.Del(name)
	}
	return out
}

func TestUpgradedRequestMatchesPlainProxyRewrite(t *testing.T) {
	plainSeen := make(chan *http.Request, 1)
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { plainSeen <- r })
	plainIng := &Ingress{log: discardLogger(), conns: nopConns{}}
	plainIng.proxy = plainIng.newProxy(0)
	plainIng.proxyHTTP(httptest.NewRecorder(), parityRequest(false), &fakeAgentConn{backend: backend}, parityPolicyEndpoint(), "/api/items", 0)
	plain := <-plainSeen

	upgradeSeen := make(chan *http.Request, 1)
	upIng := &Ingress{log: discardLogger(), conns: nopConns{}}
	upIng.upgrades = newUpgradeTracker(upIng, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "203.0.113.9:4321"
		upIng.proxyUpgrade(w, r, capturingUpgradeAgent{seen: upgradeSeen}, parityPolicyEndpoint(), "/api/items", 0)
	}))
	t.Cleanup(srv.Close)
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	wire := parityRequest(true)
	wire.RequestURI = ""
	if err := wire.Write(c); err != nil {
		t.Fatal(err)
	}
	var upgraded *http.Request
	select {
	case upgraded = <-upgradeSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never received the upgraded request")
	}

	if upgraded.URL.Path != plain.URL.Path || upgraded.URL.RawQuery != plain.URL.RawQuery {
		t.Fatalf("upgrade target = %q?%q, plain = %q?%q", upgraded.URL.Path, upgraded.URL.RawQuery, plain.URL.Path, plain.URL.RawQuery)
	}
	if upgraded.Host != plain.Host {
		t.Fatalf("upgrade host = %q, plain = %q", upgraded.Host, plain.Host)
	}
	if got, want := comparableHeaders(upgraded.Header), comparableHeaders(plain.Header); !reflect.DeepEqual(got, want) {
		t.Fatalf("upgrade headers = %v\nplain headers   = %v", got, want)
	}
}
