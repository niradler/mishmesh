package gateway

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func privateRequest(t *testing.T, name string, pol *store.EndpointPolicy) tunnel.EndpointRequest {
	t.Helper()
	req := tunnel.EndpointRequest{Ref: "p", Name: name, Private: true, Kind: store.KindTCP, Lifecycle: store.LifecycleReserved}
	if pol != nil {
		raw, err := json.Marshal(pol)
		if err != nil {
			t.Fatal(err)
		}
		req.Policy = raw
	}
	return req
}

func TestReRegisterPreservesControlPlanePolicy(t *testing.T) {
	tests := []struct {
		name     string
		declared *store.EndpointPolicy
	}{
		{name: "declares nothing"},
		{name: "declares identity not required", declared: &store.EndpointPolicy{Identity: &store.EndpointIdentity{BrowserLogin: true}}},
		{name: "declares empty policy", declared: &store.EndpointPolicy{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, ag1, _, data := newRegisterFixture(t, Options{Ports: &fakePorts{}})
			ctx := context.Background()
			ack := g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{privateRequest(t, "db", nil)}})
			if ack.Endpoints[0].Error != "" {
				t.Fatalf("first register: %s", ack.Endpoints[0].Error)
			}
			ep, err := data.GetEndpoint(ctx, ack.Endpoints[0].EndpointID)
			if err != nil {
				t.Fatal(err)
			}
			creds := map[string]store.EndpointCredential{"c": {Header: "Authorization", Secret: "org-secret"}}
			ep.Policy.Identity = &store.EndpointIdentity{Required: true, Credentials: creds}
			ep.Policy.AccessPolicy = &store.AccessPolicy{AllowEmails: []string{"a@b.c"}}
			ep.Policy.ProxyTargets = []string{"10.0.0.5:80"}
			ep.Policy.LoadBalance = store.LoadBalanceFailover
			if err := data.UpdateEndpoint(ctx, ep); err != nil {
				t.Fatal(err)
			}
			ack = g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{privateRequest(t, "db", tc.declared)}})
			if ack.Endpoints[0].Error != "" {
				t.Fatalf("re-register: %s", ack.Endpoints[0].Error)
			}
			got, err := data.GetEndpoint(ctx, ep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Policy.Identity == nil || !got.Policy.Identity.Required {
				t.Errorf("identity.required loosened: %+v", got.Policy.Identity)
			}
			if got.Policy.Identity != nil && !reflect.DeepEqual(got.Policy.Identity.Credentials, creds) {
				t.Errorf("credentials lost: %+v", got.Policy.Identity.Credentials)
			}
			if got.Policy.AccessPolicy == nil || got.Policy.AccessPolicy.AllowEmails[0] != "a@b.c" {
				t.Errorf("access policy lost: %+v", got.Policy.AccessPolicy)
			}
			if !reflect.DeepEqual(got.Policy.ProxyTargets, []string{"10.0.0.5:80"}) || got.Policy.LoadBalance != store.LoadBalanceFailover {
				t.Errorf("control-plane routing fields lost: %+v", got.Policy)
			}
		})
	}
}

func TestReRegisterStillAppliesAgentSettableFields(t *testing.T) {
	g, ag1, _, data := newRegisterFixture(t, Options{Ports: &fakePorts{}})
	ctx := context.Background()
	first := g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{privateRequest(t, "db", &store.EndpointPolicy{IPAllow: []string{"10.0.0.0/8"}})}})
	declared := &store.EndpointPolicy{IPAllow: []string{"192.168.0.0/16"}, Connect: &store.ConnectPolicy{Name: "db", AllowAgents: []string{"*"}}}
	g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{privateRequest(t, "db", declared)}})
	got, err := data.GetEndpoint(ctx, first.Endpoints[0].EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Policy.IPAllow, []string{"192.168.0.0/16"}) || len(got.Policy.Connect.AllowAgents) != 1 {
		t.Fatalf("agent-settable fields not applied: %+v", got.Policy)
	}
}

func TestOtherAgentCannotTakeOverPrivateEndpoint(t *testing.T) {
	g, ag1, ag2, data := newRegisterFixture(t, Options{Ports: &fakePorts{}})
	ctx := context.Background()
	victim := &store.EndpointPolicy{Connect: &store.ConnectPolicy{Name: "db", AllowAgents: []string{"client"}}}
	first := g.handleRegister(ctx, ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{privateRequest(t, "db", victim)}})
	epID := first.Endpoints[0].EndpointID
	attacker := &store.EndpointPolicy{Connect: &store.ConnectPolicy{Name: "db", AllowAgents: []string{"*"}}}
	for _, ref := range []string{"db", epID} {
		req := privateRequest(t, ref, attacker)
		ack := g.handleRegister(ctx, ag2, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{req}})
		if ack.Endpoints[0].Error == "" {
			t.Errorf("agent2 took over private endpoint via name %q", ref)
		}
	}
	got, err := data.GetEndpoint(ctx, epID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != ag1.ID || got.Policy.Connect.AllowAgents[0] != "client" {
		t.Fatalf("endpoint was altered: agent=%s policy=%+v", got.AgentID, got.Policy)
	}
}
