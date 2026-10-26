package health

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeServices map[string]string

func (f fakeServices) ServiceState(id string) (string, string, bool) {
	s, ok := f[id]
	return id, s, ok
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	holder, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := ln.Accept()
	if err != nil {
		holder.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		holder.Close()
		accepted.Close()
	})
	return holder.LocalAddr().String()
}

func TestClosedAddrRefuses(t *testing.T) {
	addr := closedAddr(t)
	if c, err := net.DialTimeout("tcp", addr, 5*time.Second); err == nil {
		c.Close()
		t.Fatalf("dial %s succeeded, want refused", addr)
	}
}

func liveAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

type fixture struct {
	data  store.DataStore
	conns store.ConnectionStore
	guard *proxy.Guard
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	data, err := sqlite.Open(t.TempDir() + "/h.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "org_default", Name: "d", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	guard, err := proxy.NewGuard(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	conns := memory.NewConnStore()
	proxy.Register(context.Background(), data, conns, nil, guard)
	return fixture{data: data, conns: conns, guard: guard}
}

func (f fixture) addEndpoint(t *testing.T, method string, pol *store.EndpointPolicy) *store.Endpoint {
	t.Helper()
	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: proxy.AgentID, OrgID: "org_default", Kind: store.KindHTTP,
		Method: method, Lifecycle: store.LifecycleReserved, Policy: pol, CreatedAt: time.Now(),
	}
	if err := f.data.CreateEndpoint(context.Background(), ep); err != nil {
		t.Fatal(err)
	}
	return ep
}

func (f fixture) prober(services ServiceStates) *Prober {
	return NewProber(Options{Data: f.data, Conns: f.conns, Guard: f.guard, Services: services, Timeout: 2 * time.Second})
}

func TestProbeProxyStates(t *testing.T) {
	live := liveAddr(t)
	live2 := liveAddr(t)
	dead := closedAddr(t)
	dead2 := closedAddr(t)
	tests := []struct {
		name     string
		targets  []string
		service  string
		services fakeServices
		want     string
	}{
		{"all up", []string{live, live2}, "", nil, StateUp},
		{"all down", []string{dead, dead2}, "", nil, StateDown},
		{"degraded", []string{live, dead}, "", nil, StateDegraded},
		{"sleeping service", []string{dead}, "svc", fakeServices{"svc": ServiceSleeping}, StateSleeping},
		{"sleeping service answering is up", []string{live}, "svc", fakeServices{"svc": ServiceSleeping}, StateUp},
		{"externally stopped service sleeps", []string{dead}, "svc", fakeServices{"svc": ServiceStopped}, StateSleeping},
		{"failed service is down", []string{dead}, "svc", fakeServices{"svc": ServiceError}, StateDown},
		{"unhealthy service is down", []string{dead}, "svc", fakeServices{"svc": ServiceUnhealthy}, StateDown},
		{"starting service", []string{dead}, "svc", fakeServices{"svc": ServiceStarting}, StateWaking},
		{"running service with dead target", []string{dead}, "svc", fakeServices{"svc": ServiceRunning}, StateDown},
		{"running service with live target", []string{live}, "svc", fakeServices{"svc": ServiceRunning}, StateUp},
		{"unconfigured service is down", []string{live}, "other", fakeServices{"svc": ServiceSleeping}, StateDown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.service != "" {
				if err := f.data.CreateService(context.Background(), &store.Service{ID: tc.service, OrgID: "org_default", Name: tc.service, Driver: "docker", Config: json.RawMessage(`{"containers":["health-test"]}`), CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			ep := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: tc.targets, ServiceID: tc.service})
			p := f.prober(tc.services)
			h, err := p.ProbeNow(context.Background(), ep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if h.State != tc.want {
				t.Fatalf("state = %q (%s) want %q", h.State, h.Detail, tc.want)
			}
			if h.Service != tc.service {
				t.Fatalf("service = %q want %q", h.Service, tc.service)
			}
			if len(h.Targets) != len(tc.targets) {
				t.Fatalf("targets = %d want %d", len(h.Targets), len(tc.targets))
			}
			if h.CheckedAt.IsZero() {
				t.Fatal("checked_at not set")
			}
			got, ok := p.Get(ep.ID)
			if !ok || got.State != tc.want {
				t.Fatalf("Get = %+v ok=%v", got, ok)
			}
		})
	}
}

func TestProbeDownCarriesReason(t *testing.T) {
	f := newFixture(t)
	ep := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{closedAddr(t)}})
	h, err := f.prober(nil).ProbeNow(context.Background(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.State != StateDown || h.Detail == "" || h.Targets[0].Error == "" || h.Targets[0].Healthy {
		t.Fatalf("unexpected: %+v", h)
	}
}

func TestProbeHTTPSCertificateProblemIsDown(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		otls *store.OriginTLS
		want string
	}{
		{"untrusted", nil, StateDown},
		{"insecure", &store.OriginTLS{InsecureSkipVerify: true}, StateUp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ep := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{"https://" + u.Host}, OriginTLS: tc.otls})
			h, err := f.prober(nil).ProbeNow(context.Background(), ep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if h.State != tc.want {
				t.Fatalf("state = %q (%s) want %q", h.State, h.Detail, tc.want)
			}
		})
	}
}

func TestProbeNonProxy(t *testing.T) {
	f := newFixture(t)
	ep := f.addEndpoint(t, store.MethodNative, nil)
	p := f.prober(nil)
	h, err := p.ProbeNow(context.Background(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.State != StateOffline {
		t.Fatalf("state = %q want offline", h.State)
	}
	f.conns.AddAgent(stubConn{})
	f.conns.BindEndpoint(ep.ID, "ag_stub")
	if h, _ = p.ProbeNow(context.Background(), ep.ID); h.State != StateUp {
		t.Fatalf("state = %q want up", h.State)
	}
}

type stubConn struct{}

func (stubConn) AgentID() string { return "ag_stub" }
func (stubConn) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (stubConn) Close() error { return nil }

func TestTargetHealthyAndForget(t *testing.T) {
	live, dead := liveAddr(t), closedAddr(t)
	f := newFixture(t)
	ep := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{live, dead}})
	p := f.prober(nil)
	if _, known := p.TargetHealthy(ep.ID, live); known {
		t.Fatal("unprobed target must be unknown")
	}
	if _, err := p.ProbeNow(context.Background(), ep.ID); err != nil {
		t.Fatal(err)
	}
	if healthy, known := p.TargetHealthy(ep.ID, live); !known || !healthy {
		t.Fatalf("live: healthy=%v known=%v", healthy, known)
	}
	if healthy, known := p.TargetHealthy(ep.ID, dead); !known || healthy {
		t.Fatalf("dead: healthy=%v known=%v", healthy, known)
	}
	if _, known := p.TargetHealthy(ep.ID, "other:1"); known {
		t.Fatal("unlisted target must be unknown")
	}
	p.Forget(ep.ID)
	if _, ok := p.Get(ep.ID); ok {
		t.Fatal("Forget must drop the entry")
	}
}

func TestProbeNowMissingEndpoint(t *testing.T) {
	f := newFixture(t)
	if _, err := f.prober(nil).ProbeNow(context.Background(), "ep_missing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunSweepsAndPrunes(t *testing.T) {
	f := newFixture(t)
	ep := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{liveAddr(t)}})
	p := NewProber(Options{Data: f.data, Conns: f.conns, Guard: f.guard, Interval: 20 * time.Millisecond, Timeout: time.Second})
	p.store("ep_gone", EndpointHealth{State: StateUp})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h, ok := p.Get(ep.ID); ok && h.State == StateUp {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h, ok := p.Get(ep.ID); !ok || h.State != StateUp {
		t.Fatalf("endpoint not probed: %+v ok=%v", h, ok)
	}
	if _, ok := p.Get("ep_gone"); ok {
		t.Fatal("stale entry not pruned")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestTriggerSweepsWithoutWaitingForTheInterval(t *testing.T) {
	f := newFixture(t)
	first := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{liveAddr(t)}})
	p := NewProber(Options{Data: f.data, Conns: f.conns, Guard: f.guard, Interval: time.Hour, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor := func(id string) bool {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := p.Get(id); ok {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}
	if !waitFor(first.ID) {
		t.Fatal("initial sweep did not run")
	}
	second := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{liveAddr(t)}})
	p.Trigger()
	if !waitFor(second.ID) {
		t.Fatal("Trigger did not sweep before the interval")
	}
}
