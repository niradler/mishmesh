package ingress

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type recordedAccess struct {
	mu     sync.Mutex
	events []*store.AccessEvent
}

func (r *recordedAccess) Record(e *store.AccessEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordedAccess) take() []*store.AccessEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func TestIngressEmitsOneAccessEventPerRequest(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_test", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	ep := &store.Endpoint{ID: "ep_1", AgentID: "ag_test", OrgID: "org_1", Kind: store.KindHTTP, Subdomain: "app", CreatedAt: time.Now(),
		Policy: &store.EndpointPolicy{Identity: &store.EndpointIdentity{Required: true}}}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	conns := memory.NewConnStore()
	conns.AddAgent(&fakeAgentConn{backend: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "hello")
	})})
	conns.BindEndpoint("ep_1", "ag_test")

	svc := newIdentityService(t)
	deciders := &fakeDeciders{decide: allowAll}
	access := &recordedAccess{}
	ing := New(Options{Data: data, Conns: conns, Log: discardLogger(), BaseDomain: "example.com", Identity: svc, Deciders: deciders, AccessLog: access})
	saToken := issueSAToken(t, svc, "org_1")

	serve := func(host, token string) int {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/secret/path?api_key=hunter2", nil)
		r.RemoteAddr = "192.0.2.10:5555"
		if token != "" {
			r.Header.Set(identityAuthHeader, "Bearer "+token)
		}
		w := httptest.NewRecorder()
		ing.ServeHTTP(w, r)
		return w.Code
	}

	if code := serve("app.example.com", saToken); code != http.StatusOK {
		t.Fatalf("allowed request = %d", code)
	}
	events := access.take()
	if len(events) != 1 {
		t.Fatalf("allowed request events = %d", len(events))
	}
	e := events[0]
	if e.Decision != store.AccessAllow || e.Status != http.StatusOK || e.OrgID != "org_1" || e.EndpointID != "ep_1" || e.AgentID != "ag_test" ||
		e.PrincipalType != string(authz.EntityServiceAccount) || e.PrincipalID != "sa_1" || e.SessionID == "" ||
		e.Path != "/secret/path" || e.Method != http.MethodGet || e.ClientIP != "192.0.2.10" || e.BytesOut != 5 || e.Source != store.AccessSourceIngress {
		t.Fatalf("allow event = %+v", e)
	}

	if code := serve("app.example.com", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", code)
	}
	events = access.take()
	if len(events) != 1 || events[0].Decision != store.AccessDeny || events[0].Reason != "unauthenticated" || events[0].PrincipalID != "" || events[0].Status != http.StatusUnauthorized {
		t.Fatalf("anonymous events = %+v", events)
	}

	deciders.decide = func(authz.DecisionRequest) (authz.Decision, error) {
		return authz.Decision{Reason: authz.ReasonPolicyDeny, Policies: []authz.DeterminingPolicy{{ID: "policy3"}}}, nil
	}
	if code := serve("app.example.com", saToken); code != http.StatusForbidden {
		t.Fatalf("denied = %d", code)
	}
	events = access.take()
	if len(events) != 1 || events[0].Decision != store.AccessDeny || events[0].Reason != string(authz.ReasonPolicyDeny) ||
		events[0].PrincipalID != "sa_1" || len(events[0].PolicyIDs) != 1 || events[0].PolicyIDs[0] != "policy3" {
		t.Fatalf("deny events = %+v", events)
	}

	if code := serve("ghost.example.com", saToken); code != http.StatusNotFound {
		t.Fatalf("unknown host = %d", code)
	}
	if events := access.take(); len(events) != 0 {
		t.Fatalf("unresolved request must not emit an org event: %+v", events)
	}
}
