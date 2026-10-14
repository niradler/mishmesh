package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeWaker struct {
	mu        sync.Mutex
	state     string
	known     bool
	hiddenOrg bool
	ensureErr error
	ensured   []string
	async     []string
	active    int
	acquired  int
}

func (f *fakeWaker) VisibleTo(orgID, _ string) bool {
	return orgID == "org_1" && !f.hiddenOrg
}

func (f *fakeWaker) ServiceState(id string) (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return id, f.state, f.known
}

func (f *fakeWaker) EnsureRunning(_ context.Context, name, ready string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, name+"@"+ready)
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.state = services.StateRunning
	return nil
}

func (f *fakeWaker) StartAsync(name, ready string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.async = append(f.async, name+"@"+ready)
}

func (f *fakeWaker) Acquire(string) func() {
	f.mu.Lock()
	f.active++
	f.acquired++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}
}

func newWakeIngress(t *testing.T, pol *store.EndpointPolicy, waker ServiceWaker) *Ingress {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "wake.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_test", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	if pol != nil && pol.ServiceID != "" {
		if err := data.CreateService(ctx, &store.Service{ID: pol.ServiceID, OrgID: "org_1", Name: pol.ServiceID, Driver: "docker", Config: json.RawMessage(`{"containers":["ingress-test"]}`), CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.CreateEndpoint(ctx, &store.Endpoint{ID: "ep_1", AgentID: "ag_test", OrgID: "org_1", Kind: store.KindHTTP, Subdomain: "app", Policy: pol, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	conns := memory.NewConnStore()
	conns.AddAgent(&fakeAgentConn{backend: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "backend")
	})})
	conns.BindEndpoint("ep_1", "ag_test")
	return New(Options{Data: data, Conns: conns, Log: discardLogger(), BaseDomain: "example.com", Services: waker})
}

func TestWakeServiceIngress(t *testing.T) {
	pol := &store.EndpointPolicy{ServiceID: "svc", ProxyTargets: []string{"127.0.0.1:9000"}}
	tests := []struct {
		name         string
		state        string
		known        bool
		hiddenOrg    bool
		ensureErr    error
		accept       string
		wantCode     int
		wantBody     string
		wantEnsured  int
		wantAsync    int
		wantAcquired int
	}{
		{"running proxies and tracks activity", services.StateRunning, true, false, nil, "text/html", http.StatusOK, "backend", 0, 0, 1},
		{"sleeping browser gets waking page", services.StateSleeping, true, false, nil, "text/html,application/xhtml+xml", http.StatusServiceUnavailable, "Starting svc", 0, 1, 0},
		{"sleeping api call is held then proxied", services.StateSleeping, true, false, nil, "application/json", http.StatusOK, "backend", 1, 0, 1},
		{"stopped api call wakes too", services.StateStopped, true, false, nil, "", http.StatusOK, "backend", 1, 0, 1},
		{"wake failure is 503 without detail", services.StateSleeping, true, false, errors.New("docker: secret path /x"), "", http.StatusServiceUnavailable, "retry shortly", 1, 0, 0},
		{"unknown service fails closed", "", false, false, nil, "", http.StatusServiceUnavailable, "service unavailable", 0, 0, 0},
		{"service no longer allowlisted for the org never wakes", services.StateSleeping, true, true, nil, "text/html", http.StatusServiceUnavailable, "service unavailable", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			waker := &fakeWaker{state: tc.state, known: tc.known, hiddenOrg: tc.hiddenOrg, ensureErr: tc.ensureErr}
			ing := newWakeIngress(t, pol, waker)
			r := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
			if tc.accept != "" {
				r.Header.Set("Accept", tc.accept)
			}
			w := httptest.NewRecorder()
			ing.ServeHTTP(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("code = %d want %d body %q", w.Code, tc.wantCode, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("body %q does not contain %q", w.Body.String(), tc.wantBody)
			}
			if strings.Contains(w.Body.String(), "secret path") {
				t.Fatal("driver error leaked to client")
			}
			if len(waker.ensured) != tc.wantEnsured || len(waker.async) != tc.wantAsync || waker.acquired != tc.wantAcquired {
				t.Fatalf("ensured=%v async=%v acquired=%d", waker.ensured, waker.async, waker.acquired)
			}
			for _, call := range append(waker.ensured, waker.async...) {
				if call != "svc@127.0.0.1:9000" {
					t.Fatalf("wake call %q did not use the first target as ready address", call)
				}
			}
			if waker.active != 0 {
				t.Fatalf("active = %d after request, release not called", waker.active)
			}
		})
	}
}

func TestWakingPageHeaders(t *testing.T) {
	waker := &fakeWaker{state: services.StateSleeping, known: true}
	ing := newWakeIngress(t, &store.EndpointPolicy{ServiceID: "svc"}, waker)
	r := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	ing.ServeHTTP(w, r)
	if w.Header().Get("Retry-After") != "2" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", w.Header())
	}
	if !strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Fatal("waking page must auto-refresh")
	}
}

func TestUnauthenticatedRequestNeverWakes(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	waker := &fakeWaker{state: services.StateSleeping, known: true}
	ing := newWakeIngress(t, &store.EndpointPolicy{ServiceID: "svc", BasicAuthUser: "u", BasicAuthHash: string(hash)}, waker)
	r := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	ing.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d want 401", w.Code)
	}
	if len(waker.ensured)+len(waker.async) != 0 {
		t.Fatal("an unauthenticated request woke the service")
	}
}

func TestWantsWakingPage(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		accept  string
		upgrade bool
		want    bool
	}{
		{"browser get", http.MethodGet, "text/html", false, true},
		{"browser head", http.MethodHead, "text/html", false, true},
		{"post form", http.MethodPost, "text/html", false, false},
		{"json api", http.MethodGet, "application/json", false, false},
		{"websocket", http.MethodGet, "text/html", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/", nil)
			r.Header.Set("Accept", tc.accept)
			if tc.upgrade {
				r.Header.Set("Connection", "Upgrade")
				r.Header.Set("Upgrade", "websocket")
			}
			if got := wantsWakingPage(r); got != tc.want {
				t.Fatalf("wantsWakingPage = %v want %v", got, tc.want)
			}
		})
	}
}
