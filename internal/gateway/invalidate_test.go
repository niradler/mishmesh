package gateway

import (
	"context"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestEndpointChangesNotifyInvalidator(t *testing.T) {
	g, ag1, _, _ := newRegisterFixture(t, Options{})
	ctx := context.Background()
	calls := 0
	g.SetEndpointInvalidator(func() { calls++ })

	g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "a", Kind: store.KindHTTP, Subdomain: "shop"}}})
	if calls != 1 {
		t.Fatalf("registration notified %d times, want 1", calls)
	}
	swept, err := g.SweepOrphanedEphemeral(ctx)
	if err != nil || swept != 1 {
		t.Fatalf("swept = %d, err = %v", swept, err)
	}
	if calls != 2 {
		t.Fatalf("sweep notified %d times in total, want 2", calls)
	}
	g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "a", Kind: store.KindHTTP, Subdomain: "shop"}}})
	g.cleanupEphemeral(ctx, ag1.ID)
	if calls != 4 {
		t.Fatalf("cleanup notified %d times in total, want 4", calls)
	}
}
