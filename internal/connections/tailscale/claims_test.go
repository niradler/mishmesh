package tailscaleconnection

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
)

type countingData struct {
	*endpointData
	connectionLists atomic.Int64
	endpointLists   atomic.Int64
}

func (d *countingData) ListAllConnections(ctx context.Context) ([]*store.Connection, error) {
	d.connectionLists.Add(1)
	return d.endpointData.ListAllConnections(ctx)
}

func (d *countingData) ListEndpointsByOrg(ctx context.Context, org string) ([]*store.Endpoint, error) {
	d.endpointLists.Add(1)
	return d.endpointData.ListEndpointsByOrg(ctx, org)
}

func TestClaimsAreSharedAcrossInstancesInSteadyState(t *testing.T) {
	const instances = 5
	_, provider, data, config, first := newFixture(t, "managed")
	counting := &countingData{endpointData: data}
	opened := []connections.Instance{first}
	for n := 1; n < instances; n++ {
		id := "extra" + strconv.Itoa(n)
		extra := config
		extra.Record.ID, extra.Record.Name = id, id
		if err := data.CreateConnection(context.Background(), &extra.Record); err != nil {
			t.Fatal(err)
		}
		data.endpoints = append(data.endpoints, &store.Endpoint{ID: "ep-" + id, OrgID: "org", Domain: id + ".example.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: id}}}})
		instance, err := provider.Open(extra, connections.Deps{Data: counting, Origin: "http://127.0.0.1:8090"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(instance.Close)
		opened = append(opened, instance)
	}
	syncAll := func() {
		for _, instance := range opened[1:] {
			if _, err := instance.Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	syncAll()
	counting.connectionLists.Store(0)
	counting.endpointLists.Store(0)
	syncAll()
	t.Logf("steady-state sync of %d instances: ListAllConnections=%d ListEndpointsByOrg=%d", instances-1, counting.connectionLists.Load(), counting.endpointLists.Load())
	if got := counting.connectionLists.Load(); got > 1 {
		t.Fatalf("ListAllConnections called %d times for one steady-state cycle", got)
	}
}
