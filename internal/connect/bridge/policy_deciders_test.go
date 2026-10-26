package bridge

import (
	"context"
	"errors"
	"testing"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

type connectPolicyDeciders struct {
	data   store.DataStore
	agents map[string]*store.Agent
}

func (d connectPolicyDeciders) Decider(identity.Scope) authz.Decider {
	return deciderFunc(func(ctx context.Context, req authz.DecisionRequest) (authz.Decision, error) {
		ep, err := d.data.GetEndpoint(ctx, req.Resource.ID)
		if err != nil {
			return authz.Decision{}, err
		}
		if ep.Policy == nil || !ep.Policy.Connect.Allows(d.agents[req.Principal.ID]) {
			return authz.Decision{Reason: authz.ReasonPolicyDeny}, nil
		}
		return authz.Decision{Allowed: true, Reason: authz.ReasonAllow}, nil
	})
}

func TestNilDecidersFailClosed(t *testing.T) {
	b := New(Options{})
	_, err := b.decide(context.Background(), &store.Agent{ID: "ag_x", OrgID: "org_a"}, "vpn", store.KindUDP)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("decide with no deciders err = %v, want ErrUnavailable", err)
	}
}
