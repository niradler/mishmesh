package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunListAllEndpoints(t *testing.T, open func(t *testing.T) store.DataStore) {
	ctx := context.Background()
	ds := open(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		org := store.NewID("org")
		if err := ds.CreateOrg(ctx, &store.Org{ID: org, Name: org, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		agent := &store.Agent{ID: store.NewID("agt"), OrgID: org, Name: "a", Status: store.AgentActive, CreatedAt: now}
		if err := ds.CreateAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
		ep := &store.Endpoint{ID: store.NewID("ep"), AgentID: agent.ID, OrgID: org, Kind: store.KindHTTP, Method: store.MethodProxy, Lifecycle: store.LifecycleReserved, CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if err := ds.CreateEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
		want[ep.ID] = true
	}
	got, err := ds.ListAllEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ep := range got {
		seen[ep.ID] = true
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("endpoint %s missing from ListAllEndpoints (got %d)", id, len(got))
		}
	}
}
