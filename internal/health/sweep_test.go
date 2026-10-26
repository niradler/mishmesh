package health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type failingOrgData struct {
	store.DataStore
	failOrg string
}

func (d failingOrgData) ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error) {
	if orgID == d.failOrg {
		return nil, errors.New("listing unavailable")
	}
	return d.DataStore.ListEndpointsByOrg(ctx, orgID)
}

func TestSweepContinuesPastFailingOrgAndKeepsItsResults(t *testing.T) {
	f := newFixture(t)
	if err := f.data.CreateOrg(context.Background(), &store.Org{ID: "org_bad", Name: "bad", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	healthy := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{liveAddr(t)}})
	p := NewProber(Options{Data: failingOrgData{DataStore: f.data, failOrg: "org_bad"}, Conns: f.conns, Guard: f.guard, Timeout: time.Second})
	p.store("ep_in_bad_org", EndpointHealth{State: StateUp})
	p.sweep(context.Background())
	if h, ok := p.Get(healthy.ID); !ok || h.State != StateUp {
		t.Fatalf("healthy org endpoint was not probed: %+v ok=%v", h, ok)
	}
	if _, ok := p.Get("ep_in_bad_org"); !ok {
		t.Fatal("results were pruned although an org listing failed")
	}
}
