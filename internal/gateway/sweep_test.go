package gateway

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type stubConn struct{ id string }

func (s stubConn) AgentID() string { return s.id }
func (stubConn) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (stubConn) Close() error { return nil }

type ownedElsewhereStore struct {
	store.ConnectionStore
	owned map[string]bool
}

func (o ownedElsewhereStore) OwnedElsewhere(agentID string) bool { return o.owned[agentID] }

func TestSweepOrphanedEphemeral(t *testing.T) {
	ctx := context.Background()
	g, ag1, ag2, data := newRegisterFixture(t, Options{})
	g.conns = ownedElsewhereStore{ConnectionStore: g.conns, owned: map[string]bool{"ag2": true}}

	mk := func(id, agentID, lifecycle string) {
		t.Helper()
		ep := &store.Endpoint{ID: id, AgentID: agentID, OrgID: "org1", Kind: store.KindHTTP, Lifecycle: lifecycle, Subdomain: id, CreatedAt: time.Now()}
		if err := data.CreateEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
	}
	mk("orphan-ephemeral", ag1.ID, store.LifecycleEphemeral)
	mk("orphan-reserved", ag1.ID, store.LifecycleReserved)
	mk("remote-ephemeral", ag2.ID, store.LifecycleEphemeral)
	g.conns.AddAgent(stubConn{id: "ag3"})
	if err := data.CreateAgent(ctx, &store.Agent{ID: "ag3", OrgID: "org1", Name: "live", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mk("live-ephemeral", "ag3", store.LifecycleEphemeral)

	swept, err := g.SweepOrphanedEphemeral(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Fatalf("swept = %d, want 1", swept)
	}
	want := map[string]bool{"orphan-ephemeral": false, "orphan-reserved": true, "remote-ephemeral": true, "live-ephemeral": true}
	for id, shouldExist := range want {
		_, err := data.GetEndpoint(ctx, id)
		if exists := err == nil; exists != shouldExist {
			t.Errorf("endpoint %s exists = %v, want %v", id, exists, shouldExist)
		}
	}
}
