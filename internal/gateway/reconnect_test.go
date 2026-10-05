package gateway

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type staleLookupStore struct {
	store.DataStore
	hideFirstLookup atomic.Bool
}

func (s *staleLookupStore) GetEndpointBySubdomain(ctx context.Context, sub string) (*store.Endpoint, error) {
	if s.hideFirstLookup.CompareAndSwap(true, false) {
		return nil, store.ErrNotFound
	}
	return s.DataStore.GetEndpointBySubdomain(ctx, sub)
}

func TestRegisterRaceOnSameSubdomainIsIdempotent(t *testing.T) {
	g, ag1, ag2, data := newRegisterFixture(t, Options{})
	ctx := context.Background()
	existing := &store.Endpoint{ID: "ep_old", AgentID: ag1.ID, OrgID: ag1.OrgID, Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral, Subdomain: "app", CreatedAt: time.Now()}
	if err := data.CreateEndpoint(ctx, existing); err != nil {
		t.Fatal(err)
	}
	stale := &staleLookupStore{DataStore: data}
	g.data = stale

	stale.hideFirstLookup.Store(true)
	ack := g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "0", Kind: store.KindHTTP, Subdomain: "app"}}})
	if got := ack.Endpoints[0]; got.Error != "" || got.EndpointID != "ep_old" {
		t.Fatalf("same agent re-register after race: %+v, want rebind of ep_old", got)
	}

	stale.hideFirstLookup.Store(true)
	ack = g.handleRegister(ctx, ag2, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "0", Kind: store.KindHTTP, Subdomain: "app"}}})
	if got := ack.Endpoints[0]; got.Error == "" {
		t.Fatalf("different agent must be refused a taken subdomain, got %+v", got)
	}
}

func TestCleanupEphemeralSkipsAgentsWithLiveSession(t *testing.T) {
	tests := []struct {
		name       string
		liveAgent  bool
		wantExists bool
	}{
		{"no live session removes ephemeral endpoints", false, false},
		{"newer live session keeps endpoints", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, ag1, _, data := newRegisterFixture(t, Options{})
			ctx := context.Background()
			ep := &store.Endpoint{ID: "ep_1", AgentID: ag1.ID, OrgID: ag1.OrgID, Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral, Subdomain: "app", CreatedAt: time.Now()}
			if err := data.CreateEndpoint(ctx, ep); err != nil {
				t.Fatal(err)
			}
			if tt.liveAgent {
				g.conns.AddAgent(stubConn{id: ag1.ID})
			}
			g.cleanupEphemeral(ctx, ag1.ID)
			_, err := data.GetEndpoint(ctx, "ep_1")
			if exists := err == nil; exists != tt.wantExists {
				t.Fatalf("endpoint exists = %v, want %v", exists, tt.wantExists)
			}
		})
	}
}

func TestOwnedElsewhereFailsSafeOnRedisError(t *testing.T) {
	g, ag1, _, _ := newRegisterFixture(t, Options{})
	g.conns = ownedElsewhereStore{ConnectionStore: g.conns, owned: map[string]bool{ag1.ID: true}}
	if !g.agentHasLiveSession(ag1.ID) {
		t.Fatal("agent owned elsewhere must count as live")
	}
}
