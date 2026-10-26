package health

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type failingListData struct {
	store.DataStore
	failAll atomic.Bool
}

func (d *failingListData) ListAllEndpoints(ctx context.Context) ([]*store.Endpoint, error) {
	if d.failAll.Load() {
		return nil, errors.New("listing unavailable")
	}
	return d.DataStore.ListAllEndpoints(ctx)
}

type countingData struct {
	store.DataStore
	listOrgs         atomic.Int32
	listEndpointsOrg atomic.Int32
	listAllEndpoints atomic.Int32
}

func (d *countingData) ListOrgs(ctx context.Context) ([]*store.Org, error) {
	d.listOrgs.Add(1)
	return d.DataStore.ListOrgs(ctx)
}

func (d *countingData) ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error) {
	d.listEndpointsOrg.Add(1)
	return d.DataStore.ListEndpointsByOrg(ctx, orgID)
}

func (d *countingData) ListAllEndpoints(ctx context.Context) ([]*store.Endpoint, error) {
	d.listAllEndpoints.Add(1)
	return d.DataStore.ListAllEndpoints(ctx)
}

func TestSweepKeepsResultsWhenListingFails(t *testing.T) {
	f := newFixture(t)
	healthy := f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{liveAddr(t)}})
	data := &failingListData{DataStore: f.data}
	p := NewProber(Options{Data: data, Conns: f.conns, Guard: f.guard, Timeout: time.Second})
	p.sweep(context.Background())
	if h, ok := p.Get(healthy.ID); !ok || h.State != StateUp {
		t.Fatalf("endpoint was not probed: %+v ok=%v", h, ok)
	}
	p.store("ep_elsewhere", EndpointHealth{State: StateUp})
	data.failAll.Store(true)
	p.sweep(context.Background())
	if _, ok := p.Get("ep_elsewhere"); !ok {
		t.Fatal("results were pruned although the endpoint listing failed")
	}
}

func TestSweepListsEndpointsInOneQuery(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 25; i++ {
		id := store.NewID("org")
		if err := f.data.CreateOrg(context.Background(), &store.Org{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	f.addEndpoint(t, store.MethodProxy, &store.EndpointPolicy{ProxyTargets: []string{liveAddr(t)}})
	data := &countingData{DataStore: f.data}
	p := NewProber(Options{Data: data, Conns: f.conns, Guard: f.guard, Timeout: time.Second})
	p.sweep(context.Background())
	listing := data.listOrgs.Load() + data.listEndpointsOrg.Load() + data.listAllEndpoints.Load()
	t.Logf("store listing calls per sweep with 26 orgs: ListOrgs=%d ListEndpointsByOrg=%d ListAllEndpoints=%d", data.listOrgs.Load(), data.listEndpointsOrg.Load(), data.listAllEndpoints.Load())
	if listing != 1 {
		t.Fatalf("store listing calls per sweep = %d, want 1", listing)
	}
}
