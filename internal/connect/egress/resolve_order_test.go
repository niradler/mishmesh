package egress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type countingDialer struct{ resolves atomic.Int32 }

func (d *countingDialer) Dial(context.Context, string, *store.OriginTLS, time.Duration) (net.Conn, error) {
	return nil, errors.New("dial not expected")
}

func (d *countingDialer) ResolveAll(context.Context, string) ([]string, error) {
	d.resolves.Add(1)
	return nil, errors.New("resolution stubbed")
}

type decideFunc func(authz.DecisionRequest) authz.Decision

func (f decideFunc) Decider(identity.Scope) authz.Decider { return f }

func (f decideFunc) Decide(_ context.Context, req authz.DecisionRequest) (authz.Decision, error) {
	return f(req), nil
}

func TestEgressAppliesPolicyBeforeResolving(t *testing.T) {
	deny := authz.Decision{Reason: authz.ReasonPolicyDeny, Policies: []authz.DeterminingPolicy{{ID: "block-host"}}}
	allow := authz.Decision{Allowed: true, Reason: authz.ReasonAllow}
	ipScoped := authz.Decision{Reason: authz.ReasonNoPolicyMatch, Scope: authz.CacheScope{ResolvedIP: true}}
	cases := []struct {
		name         string
		decision     authz.Decision
		wantStatus   tunnel.EgressStatus
		wantReason   string
		wantResolves int32
	}{
		{"denied hostname is never resolved", deny, tunnel.EgressDenied, string(authz.ReasonPolicyDeny), 0},
		{"allowed hostname is resolved", allow, tunnel.EgressDialFailed, reasonResolve, 1},
		{"policy on resolved ip defers to resolution", ipScoped, tunnel.EgressDialFailed, reasonResolve, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dialer := &countingDialer{}
			access := &recordedEvents{}
			var hostnameOnly atomic.Bool
			deciders := decideFunc(func(req authz.DecisionRequest) authz.Decision {
				hostnameOnly.Store(req.Context.ResolvedIP == "")
				return tc.decision
			})
			h := New(Options{Conns: memory.NewConnStore(), Deciders: deciders, Dialer: dialer, Access: access, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			agentSide, serverSide := net.Pipe()
			go h.HandleEgress(context.Background(), &store.Agent{ID: "ag_1", OrgID: "org_1"}, serverSide, tunnel.StreamInit{Kind: store.KindTCP, Meta: map[string]string{tunnel.MetaEgress: "blocked.example.com:443"}})
			_ = agentSide.SetDeadline(time.Now().Add(5 * time.Second))
			reply, err := tunnel.ReadEgressReply(agentSide)
			if err != nil || reply.Status != tc.wantStatus || reply.Reason != tc.wantReason {
				t.Fatalf("reply %+v err %v, want %v %s", reply, err, tc.wantStatus, tc.wantReason)
			}
			_, _ = io.ReadAll(agentSide)
			if got := dialer.resolves.Load(); got != tc.wantResolves {
				t.Fatalf("resolutions = %d, want %d", got, tc.wantResolves)
			}
			if !hostnameOnly.Load() {
				t.Fatal("pre-resolution decision must not carry a resolved ip")
			}
			if len(access.events) != 1 || access.events[0].Decision != store.AccessDeny {
				t.Fatalf("access events %+v", access.events)
			}
			if tc.wantResolves == 0 && (len(access.events[0].PolicyIDs) != 1 || access.events[0].PolicyIDs[0] != "block-host") {
				t.Fatalf("policy ids %v", access.events[0].PolicyIDs)
			}
		})
	}
}
