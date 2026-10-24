package ingress

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type switchDeciders struct{ deny atomic.Bool }

func (s *switchDeciders) Decider(identity.Scope) authz.Decider {
	return deciderFunc(func(context.Context, authz.DecisionRequest) (authz.Decision, error) {
		if s.deny.Load() {
			return authz.Decision{Reason: authz.ReasonPolicyDeny}, nil
		}
		return authz.Decision{Allowed: true, Reason: authz.ReasonAllow}, nil
	})
}

type reevaluateFixture struct {
	data     store.DataStore
	deciders *switchDeciders
	svc      *identity.Service
	ing      *Ingress
	ep       *store.Endpoint
	addr     string
}

func newReevaluateFixture(t *testing.T) *reevaluateFixture {
	t.Helper()
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "reevaluate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	ep := identityEndpoint(store.EndpointIdentity{Required: true})
	ep.AgentID, ep.Lifecycle, ep.Subdomain, ep.CreatedAt = "ag_1", store.LifecycleEphemeral, "demo", time.Now()
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	f := &reevaluateFixture{data: data, deciders: &switchDeciders{}, svc: newIdentityService(t), ep: ep}
	f.ing = &Ingress{log: discardLogger(), conns: nopConns{}, data: data, identity: f.svc, deciders: f.deciders}
	f.ing.upgrades = newUpgradeTracker(f.ing, f.svc)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, ok := f.ing.applyIdentityGate(w, r, ep, "/ws")
		if !ok {
			return
		}
		f.ing.proxyUpgrade(w, r, upgradeEchoAgent{}, ep, "/ws", 0)
	}))
	t.Cleanup(srv.Close)
	f.addr = srv.Listener.Addr().String()
	return f
}

func (f *reevaluateFixture) openUpgrade(t *testing.T) (func() error, *http.Response) {
	t.Helper()
	c, br, resp := dialUpgrade(t, f.addr, issueSAToken(t, f.svc, "org_1"))
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	return func() error {
		_, err := io.ReadAll(br)
		return err
	}, resp
}

func TestUpgradeClosedWhenPolicyNowDenies(t *testing.T) {
	f := newReevaluateFixture(t)
	drained, _ := f.openUpgrade(t)
	f.deciders.deny.Store(true)
	f.svc.FlushAll()
	if err := drained(); err != nil {
		t.Fatalf("upgrade not closed after the policy started denying: %v", err)
	}
}

func TestUpgradeClosedWhenEndpointDeleted(t *testing.T) {
	f := newReevaluateFixture(t)
	drained, _ := f.openUpgrade(t)
	if err := f.data.DeleteEndpoint(context.Background(), f.ep.ID); err != nil {
		t.Fatal(err)
	}
	f.svc.FlushAll()
	if err := drained(); err != nil {
		t.Fatalf("upgrade not closed after its endpoint was deleted: %v", err)
	}
}

func TestUpgradeSurvivesFlushWhenStillAllowed(t *testing.T) {
	f := newReevaluateFixture(t)
	c, br, resp := dialUpgrade(t, f.addr, issueSAToken(t, f.svc, "org_1"))
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	f.svc.FlushAll()
	f.ing.upgrades.WaitReevaluated()
	if _, err := io.WriteString(c, "pong"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "pong" {
		t.Fatalf("echo after flush = %q, %v", got, err)
	}
}
