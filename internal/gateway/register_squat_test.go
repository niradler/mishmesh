package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestHostEndpointCannotSquatAnotherAgentsConnectName(t *testing.T) {
	g, ag1, ag2, _ := newRegisterFixture(t, Options{Ports: &fakePorts{}})
	ctx := context.Background()
	g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{privateRequest(t, "db", nil)}})
	raw, err := json.Marshal(&store.EndpointPolicy{Connect: &store.ConnectPolicy{Name: "db", AllowAgents: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	ack := g.handleRegister(ctx, ag2, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "h", Kind: store.KindHTTP, Subdomain: "squat", Policy: raw}}})
	if ack.Endpoints[0].Error == "" {
		t.Fatal("http endpoint registered with a connect name owned by another agent")
	}
}
