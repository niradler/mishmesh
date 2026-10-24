package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type recordingDeciders struct {
	mu       sync.Mutex
	decision authz.Decision
	err      error
	scopes   []identity.Scope
	requests []authz.DecisionRequest
}

func (r *recordingDeciders) Decider(scope identity.Scope) authz.Decider {
	r.mu.Lock()
	r.scopes = append(r.scopes, scope)
	r.mu.Unlock()
	return deciderFunc(func(_ context.Context, req authz.DecisionRequest) (authz.Decision, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.requests = append(r.requests, req)
		return r.decision, r.err
	})
}

type deciderFunc func(context.Context, authz.DecisionRequest) (authz.Decision, error)

func (f deciderFunc) Decide(ctx context.Context, req authz.DecisionRequest) (authz.Decision, error) {
	return f(ctx, req)
}

type fakeRevocations struct {
	mu   sync.Mutex
	subs []func(identity.Revocation)
}

func (f *fakeRevocations) Subscribe(fn func(identity.Revocation)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, fn)
	return func() {}
}

func (f *fakeRevocations) publish(rev identity.Revocation) {
	f.mu.Lock()
	subs := append([]func(identity.Revocation){}, f.subs...)
	f.mu.Unlock()
	for _, fn := range subs {
		fn(rev)
	}
}

type decideFixture struct {
	data   store.DataStore
	conns  store.ConnectionStore
	origin *echoOrigin
	epID   string
	agent  *store.Agent
}

func newDecideFixture(t *testing.T) *decideFixture {
	t.Helper()
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "decide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	origin := &echoOrigin{id: "ag_origin", kinds: make(chan string, 8)}
	conns.AddAgent(origin)
	if err := data.CreateOrg(ctx, &store.Org{ID: "org_a", Name: "a", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateAgent(ctx, &store.Agent{ID: origin.id, OrgID: "org_a", Name: "office", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: origin.id, OrgID: "org_a", Kind: store.KindTCP, Method: store.MethodNative,
		Lifecycle: store.LifecycleReserved, CreatedAt: time.Now(),
		Policy: &store.EndpointPolicy{Connect: &store.ConnectPolicy{Name: "db"}},
	}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	conns.BindEndpoint(ep.ID, origin.id)
	return &decideFixture{data: data, conns: conns, origin: origin, epID: ep.ID, agent: &store.Agent{ID: "ag_laptop", OrgID: "org_a", Name: "laptop"}}
}

func (f *decideFixture) bridge(dec identity.Deciders, rev Revocations) *Bridge {
	return New(Options{Data: f.data, Conns: f.conns, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Deciders: dec, Revocations: rev})
}

func TestDecideConnectUsesCedarDecider(t *testing.T) {
	f := newDecideFixture(t)
	tests := []struct {
		name        string
		decision    authz.Decision
		err         error
		connect     string
		action      string
		wantAllowed bool
		wantReason  string
		wantTTL     int
	}{
		{"allow", authz.Decision{Allowed: true, Reason: authz.ReasonAllow, TTLSeconds: 60}, nil, "db", tunnel.DecisionActionConnect, true, string(authz.ReasonAllow), 60},
		{"policy deny", authz.Decision{Reason: authz.ReasonPolicyDeny}, nil, "db", tunnel.DecisionActionConnect, false, string(authz.ReasonPolicyDeny), 0},
		{"decider error fails closed", authz.Decision{Allowed: true}, errors.New("boom"), "db", tunnel.DecisionActionConnect, false, string(authz.ReasonUnavailable), 0},
		{"unavailable reason fails closed", authz.Decision{Allowed: true, Reason: authz.ReasonUnavailable}, nil, "db", tunnel.DecisionActionConnect, false, string(authz.ReasonUnavailable), 0},
		{"unknown endpoint is a plain deny", authz.Decision{Allowed: true}, nil, "nope", tunnel.DecisionActionConnect, false, string(authz.ReasonPolicyDeny), 0},
		{"unsupported action", authz.Decision{Allowed: true}, nil, "db", "agent:other", false, string(authz.ReasonInvalidRequest), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := &recordingDeciders{decision: tt.decision, err: tt.err}
			b := f.bridge(dec, nil)
			got := b.DecideConnect(context.Background(), f.agent, tunnel.DecisionQuery{ID: 1, Action: tt.action, Connect: tt.connect, Kind: store.KindTCP})
			if got.Allowed != tt.wantAllowed || got.Reason != tt.wantReason || got.TTLSeconds != tt.wantTTL {
				t.Fatalf("answer = %+v, want allowed=%v reason=%q ttl=%d", got, tt.wantAllowed, tt.wantReason, tt.wantTTL)
			}
		})
	}
}

func TestDecideBuildsAgentAndUserRequests(t *testing.T) {
	f := newDecideFixture(t)
	dec := &recordingDeciders{decision: authz.Decision{Allowed: true, Reason: authz.ReasonAllow}}
	b := f.bridge(dec, nil)

	if _, err := b.decide(context.Background(), f.agent, "db", store.KindTCP); err != nil {
		t.Fatal(err)
	}
	authTime := time.Unix(1_700_000_000, 0)
	user := identity.Principal{OrgID: "org_a", Ref: authz.EntityRef{Type: authz.EntityUser, ID: "usr_1"}, SessionID: "ses_1", AuthMethod: authz.AuthMethodOIDC, AuthTime: authTime}
	if _, err := b.decide(identity.WithPrincipal(context.Background(), user), f.agent, "db", store.KindTCP); err != nil {
		t.Fatal(err)
	}

	if len(dec.requests) != 2 {
		t.Fatalf("requests = %d", len(dec.requests))
	}
	agentReq, userReq := dec.requests[0], dec.requests[1]
	if agentReq.Principal != (authz.EntityRef{Type: authz.EntityAgent, ID: "ag_laptop"}) || agentReq.Context.AuthMethod != authz.AuthMethodAgentToken {
		t.Fatalf("agent request = %+v", agentReq)
	}
	if agentReq.Resource != (authz.EntityRef{Type: authz.EntityEndpoint, ID: f.epID}) || agentReq.Action != authz.ActionAgentConnect || agentReq.Context.Protocol != store.KindTCP {
		t.Fatalf("agent request = %+v", agentReq)
	}
	if dec.scopes[0] != (identity.Scope{OrgID: "org_a", AgentID: "ag_laptop"}) {
		t.Fatalf("agent scope = %+v", dec.scopes[0])
	}
	if userReq.Principal != user.Ref || userReq.Context.AuthMethod != authz.AuthMethodOIDC || userReq.Context.AuthTime == nil || !userReq.Context.AuthTime.Equal(authTime) {
		t.Fatalf("user request = %+v", userReq)
	}
	if dec.scopes[1] != (identity.Scope{OrgID: "org_a", AgentID: "ag_laptop", TokenSessionID: "ses_1"}) {
		t.Fatalf("user scope = %+v", dec.scopes[1])
	}
}

func TestDecideCrossOrgAgentCannotResolveEndpoint(t *testing.T) {
	f := newDecideFixture(t)
	dec := &recordingDeciders{decision: authz.Decision{Allowed: true}}
	b := f.bridge(dec, nil)
	outsider := &store.Agent{ID: "ag_out", OrgID: "org_b", Name: "laptop"}
	if _, err := b.decide(context.Background(), outsider, "db", store.KindTCP); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	if len(dec.requests) != 0 {
		t.Fatal("decider consulted for a foreign endpoint")
	}
}

func TestRevocationClosesBoundFlows(t *testing.T) {
	f := newDecideFixture(t)
	rev := &fakeRevocations{}
	b := f.bridge(&recordingDeciders{decision: authz.Decision{Allowed: true, Reason: authz.ReasonAllow}}, rev)
	user := identity.Principal{OrgID: "org_a", Ref: authz.EntityRef{Type: authz.EntityUser, ID: "usr_1"}, SessionID: "ses_1", AuthMethod: authz.AuthMethodPassword, AuthTime: time.Now()}
	ctx := identity.WithPrincipal(context.Background(), user)
	init := tunnel.StreamInit{Kind: store.KindTCP, Meta: map[string]string{tunnel.MetaConnect: "db"}}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { b.HandleConnect(ctx, f.agent, server, init); close(done) }()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}

	rev.publish(identity.Revocation{OrgID: "org_b", SessionIDs: []string{"ses_1"}})
	rev.publish(identity.Revocation{OrgID: "org_a", SessionIDs: []string{"ses_other"}})
	select {
	case <-done:
		t.Fatal("flow closed by unrelated revocation")
	case <-time.After(100 * time.Millisecond):
	}

	rev.publish(identity.Revocation{OrgID: "org_a", Subject: user.Ref, SessionIDs: []string{"ses_1"}})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("flow not closed after revocation")
	}
	b.mu.Lock()
	remaining := len(b.flows)
	b.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("flows = %d after close", remaining)
	}
}

func TestRevokedFlowNeverSplices(t *testing.T) {
	f := newDecideFixture(t)
	b := f.bridge(nil, nil)
	fl := &flow{orgID: "org_a", sessionID: "ses_1"}
	untrack := b.trackFlow(fl)
	defer untrack()
	b.revoke(identity.Revocation{OrgID: "org_a", SessionIDs: []string{"ses_1"}})
	near, far := net.Pipe()
	defer far.Close()
	if b.attach(fl, near) {
		t.Fatal("attach succeeded on a revoked flow")
	}
}
