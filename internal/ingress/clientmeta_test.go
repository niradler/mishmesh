package ingress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type metaRecordingAgent struct {
	mu    sync.Mutex
	metas []map[string]string
	seen  chan struct{}
}

func newMetaRecordingAgent() *metaRecordingAgent {
	return &metaRecordingAgent{seen: make(chan struct{}, 8)}
}

func (a *metaRecordingAgent) AgentID() string { return "ag_meta" }
func (a *metaRecordingAgent) Close() error    { return nil }

func (a *metaRecordingAgent) OpenStream(_ context.Context, _, kind string, meta map[string]string) (net.Conn, error) {
	a.mu.Lock()
	a.metas = append(a.metas, meta)
	a.mu.Unlock()
	a.seen <- struct{}{}
	near, far := net.Pipe()
	go func() {
		defer far.Close()
		if kind == store.KindHTTP {
			req, err := http.ReadRequest(bufio.NewReader(far))
			if err != nil {
				return
			}
			rec := httptest.NewRecorder()
			rec.WriteHeader(http.StatusNoContent)
			_ = req.Body.Close()
			_ = rec.Result().Write(far)
			return
		}
		_, _ = io.Copy(far, far)
	}()
	return near, nil
}

func (a *metaRecordingAgent) lastMeta(t *testing.T) map[string]string {
	t.Helper()
	select {
	case <-a.seen:
	case <-time.After(3 * time.Second):
		t.Fatal("OpenStream was never called")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.metas[len(a.metas)-1]
}

func TestHTTPForwardsClientIPMeta(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}
	tests := []struct {
		name    string
		remote  string
		xff     string
		trusted []*net.IPNet
		want    string
	}{
		{"direct peer", "203.0.113.9:4000", "", nil, "203.0.113.9"},
		{"untrusted peer cannot spoof xff", "203.0.113.9:4000", "6.6.6.6", trusted, "203.0.113.9"},
		{"trusted proxy yields forwarded client", "10.1.2.3:5000", "198.51.100.7", trusted, "198.51.100.7"},
		{"trusted chain skips trusted hops", "10.1.2.3:5000", "198.51.100.7, 10.9.9.9", trusted, "198.51.100.7"},
		{"ipv6 peer", "[2001:db8::1]:4000", "", nil, "2001:db8::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := newMetaRecordingAgent()
			ing := &Ingress{log: discardLogger(), conns: nopConns{}, trusted: tc.trusted}
			ing.proxy = ing.newProxy(0)
			ep := &store.Endpoint{ID: "ep_1", OrgID: "org_1", Kind: store.KindHTTP}
			r := httptest.NewRequest("GET", "http://demo.localhost/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			ing.proxyHTTP(httptest.NewRecorder(), r, agent, ep, "/", 0)
			if got := agent.lastMeta(t)[tunnel.MetaClientIP]; got != tc.want {
				t.Fatalf("client_ip = %q want %q", got, tc.want)
			}
		})
	}
}

func TestTCPForwardsClientIPMeta(t *testing.T) {
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.CreateOrg(ctx, &store.Org{ID: "org_meta", Name: "meta", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateAgent(ctx, &store.Agent{ID: "ag_meta", OrgID: "org_meta", Name: "m", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: "ep_meta", AgentID: "ag_meta", OrgID: "org_meta", Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, CreatedAt: time.Now()}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	agent := newMetaRecordingAgent()
	conns := memory.NewConnStore()
	conns.AddAgent(agent)
	conns.BindEndpoint(ep.ID, "ag_meta")

	tcp := NewTCP(TCPOptions{Conns: conns, Data: data, Log: discardLogger(), BindHost: "127.0.0.1", PortMin: 24400, PortMax: 24500})
	t.Cleanup(tcp.Shutdown)
	port, err := tcp.Open(ep.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := agent.lastMeta(t)[tunnel.MetaClientIP]; got != "127.0.0.1" {
		t.Fatalf("client_ip = %q want 127.0.0.1", got)
	}
}

func TestWithClientIPKeepsExistingMeta(t *testing.T) {
	in := map[string]string{tunnel.MetaIdleTimeout: "30s"}
	out := withClientIP(in, "192.0.2.1")
	if out[tunnel.MetaIdleTimeout] != "30s" || out[tunnel.MetaClientIP] != "192.0.2.1" {
		t.Fatalf("unexpected meta %v", out)
	}
	if _, mutated := in[tunnel.MetaClientIP]; mutated {
		t.Fatal("input meta was mutated")
	}
	if got := withClientIP(in, ""); len(got) != 1 {
		t.Fatalf("empty ip must leave meta untouched, got %v", got)
	}
}
