package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

const deciderPolicy = `
@id("ops-only")
permit (principal in Group::"grp_ops", action == Action::"http:request", resource);

@id("connect")
permit (principal, action == Action::"agent:connect", resource)
when { resource.connect_any_agent || resource.connect_agents.contains(context.agent) };
`

type deciderFixture struct {
	api *API
	now time.Time
}

func newDeciderFixture(t *testing.T, data store.DataStore) deciderFixture {
	t.Helper()
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now().UTC().Truncate(time.Second)
	api.now = func() time.Time { return now }
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, org := range []string{"org_a", "org_b"} {
		must(data.CreateOrg(ctx, &store.Org{ID: org, Name: org, CreatedAt: now}))
	}
	must(data.SetOrgPolicy(ctx, &store.OrgPolicy{OrgID: "org_a", CedarSrc: deciderPolicy, UpdatedAt: now}))
	for _, u := range []struct{ id, org string }{{"usr_ops", "org_a"}, {"usr_dev", "org_a"}, {"usr_b", "org_b"}} {
		must(data.CreateUser(ctx, &store.User{ID: u.id, Email: u.id + "@example.com", Name: u.id, CreatedAt: now}))
		must(data.CreateMembership(ctx, &store.Membership{OrgID: u.org, UserID: u.id, Role: store.RoleMember, CreatedAt: now}))
	}
	must(data.CreateGroup(ctx, &store.Group{ID: "grp_ops", OrgID: "org_a", Name: "ops", CreatedAt: now}))
	must(data.AddGroupMember(ctx, &store.GroupMember{OrgID: "org_a", GroupID: "grp_ops", MemberType: store.GroupMemberUser, MemberID: "usr_ops", CreatedAt: now}))
	for _, ag := range []struct{ id, org, name, status string }{
		{"ag_a", "org_a", "laptop", store.AgentActive},
		{"ag_named", "org_a", "builder", store.AgentActive},
		{"ag_off", "org_a", "old", store.AgentRevoked},
		{"ag_b", "org_b", "other", store.AgentActive},
	} {
		must(data.CreateAgent(ctx, &store.Agent{ID: ag.id, OrgID: ag.org, Name: ag.name, Status: ag.status, CreatedAt: now}))
	}
	for _, ep := range []struct {
		id, org, agent string
		connect        []string
	}{
		{"ep_a", "org_a", "ag_a", []string{"ag_a", "builder"}},
		{"ep_any", "org_a", "ag_a", []string{store.ConnectAnyAgent}},
		{"ep_b", "org_b", "ag_b", nil},
	} {
		e := &store.Endpoint{ID: ep.id, OrgID: ep.org, AgentID: ep.agent, Kind: "http", Subdomain: ep.id, CreatedAt: now}
		if ep.connect != nil {
			e.Policy = &store.EndpointPolicy{Connect: &store.ConnectPolicy{AllowAgents: ep.connect}}
		}
		must(data.CreateEndpoint(ctx, e))
	}
	for _, s := range []struct {
		hash, user, org string
		expires         time.Time
	}{
		{"sess_live", "usr_ops", "org_a", now.Add(time.Hour)},
		{"sess_old", "usr_ops", "org_a", now.Add(-time.Minute)},
		{"sess_other", "usr_dev", "org_a", now.Add(time.Hour)},
	} {
		must(data.CreateSession(ctx, &store.Session{IDHash: s.hash, UserID: s.user, OrgID: s.org, CreatedAt: now, ExpiresAt: s.expires}))
	}
	return deciderFixture{api: api, now: now}
}

func httpReq(principal authz.EntityRef, endpoint string) authz.DecisionRequest {
	return authz.DecisionRequest{
		Principal: principal,
		Action:    authz.ActionHTTPRequest,
		Resource:  authz.EntityRef{Type: authz.EntityEndpoint, ID: endpoint},
		Context:   authz.RequestContext{ClientIP: "203.0.113.7", Host: "a.example.com", Method: "GET", Path: "/", Protocol: "http", AuthMethod: authz.AuthMethodOIDC},
	}
}

func connectReq(agent, endpoint string) authz.DecisionRequest {
	return authz.DecisionRequest{
		Principal: authz.EntityRef{Type: authz.EntityAgent, ID: agent},
		Action:    authz.ActionAgentConnect,
		Resource:  authz.EntityRef{Type: authz.EntityEndpoint, ID: endpoint},
		Context:   authz.RequestContext{ClientIP: "10.0.0.5", Protocol: "tcp", AuthMethod: authz.AuthMethodAgentToken},
	}
}

func user(id string) authz.EntityRef { return authz.EntityRef{Type: authz.EntityUser, ID: id} }

func TestServerDecider(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "decider.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	f := newDeciderFixture(t, data)
	orgA := DecisionScope{OrgID: "org_a"}

	tests := []struct {
		name       string
		scope      DecisionScope
		req        authz.DecisionRequest
		allowed    bool
		reason     authz.Reason
		wantPolicy string
	}{
		{"group member allowed", orgA, httpReq(user("usr_ops"), "ep_a"), true, authz.ReasonAllow, "ops-only"},
		{"non group member no match", orgA, httpReq(user("usr_dev"), "ep_a"), false, authz.ReasonNoPolicyMatch, ""},
		{"user from other org unknown", orgA, httpReq(user("usr_b"), "ep_a"), false, authz.ReasonUnknownEntity, ""},
		{"endpoint from other org unknown", orgA, httpReq(user("usr_ops"), "ep_b"), false, authz.ReasonUnknownEntity, ""},
		{"missing endpoint unknown", orgA, httpReq(user("usr_ops"), "ep_missing"), false, authz.ReasonUnknownEntity, ""},
		{"service accounts unknown in m0", orgA, httpReq(authz.EntityRef{Type: authz.EntityServiceAccount, ID: "sa_1"}, "ep_a"), false, authz.ReasonUnknownEntity, ""},
		{"anonymous no match", orgA, httpReq(authz.EntityRef{Type: authz.EntityAnonymous, ID: authz.AnonymousID}, "ep_a"), false, authz.ReasonNoPolicyMatch, ""},
		{"invalid request", orgA, authz.DecisionRequest{}, false, authz.ReasonInvalidRequest, ""},
		{"live session allowed", DecisionScope{OrgID: "org_a", SessionIDHash: "sess_live"}, httpReq(user("usr_ops"), "ep_a"), true, authz.ReasonAllow, "ops-only"},
		{"expired session", DecisionScope{OrgID: "org_a", SessionIDHash: "sess_old"}, httpReq(user("usr_ops"), "ep_a"), false, authz.ReasonSessionExpired, ""},
		{"missing session revoked", DecisionScope{OrgID: "org_a", SessionIDHash: "sess_gone"}, httpReq(user("usr_ops"), "ep_a"), false, authz.ReasonSessionRevoked, ""},
		{"session of another user revoked", DecisionScope{OrgID: "org_a", SessionIDHash: "sess_other"}, httpReq(user("usr_ops"), "ep_a"), false, authz.ReasonSessionRevoked, ""},
		{"agent allowed by id", DecisionScope{OrgID: "org_a", AgentID: "ag_a"}, connectReq("ag_a", "ep_a"), true, authz.ReasonAllow, "connect"},
		{"agent allowed by name", DecisionScope{OrgID: "org_a", AgentID: "ag_named"}, connectReq("ag_named", "ep_a"), true, authz.ReasonAllow, "connect"},
		{"any agent endpoint", DecisionScope{OrgID: "org_a", AgentID: "ag_named"}, connectReq("ag_named", "ep_any"), true, authz.ReasonAllow, "connect"},
		{"revoked agent disabled", DecisionScope{OrgID: "org_a", AgentID: "ag_off"}, connectReq("ag_off", "ep_any"), false, authz.ReasonAgentDisabled, ""},
		{"agent from other org unknown", DecisionScope{OrgID: "org_a", AgentID: "ag_b"}, connectReq("ag_b", "ep_any"), false, authz.ReasonUnknownEntity, ""},
		{"acting agent must match principal", DecisionScope{OrgID: "org_a", AgentID: "ag_named"}, connectReq("ag_a", "ep_a"), false, authz.ReasonUnknownEntity, ""},
		{"acting agent required", orgA, connectReq("ag_a", "ep_a"), false, authz.ReasonUnknownEntity, ""},
		{"other org scope cannot see org a", DecisionScope{OrgID: "org_b"}, httpReq(user("usr_ops"), "ep_a"), false, authz.ReasonUnknownEntity, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := f.api.Decider(tc.scope).Decide(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != tc.allowed || d.Reason != tc.reason {
				t.Fatalf("decision = %+v, want allowed=%v reason=%s", d, tc.allowed, tc.reason)
			}
			wantOwner := tc.scope.OrgID
			if wantOwner == "org_b" {
				wantOwner = authz.OwnerDefault
			}
			if d.Owner != wantOwner {
				t.Fatalf("owner = %q, want %q", d.Owner, wantOwner)
			}
			if tc.wantPolicy != "" && (len(d.Policies) != 1 || d.Policies[0].ID != tc.wantPolicy) {
				t.Fatalf("policies = %+v, want %s", d.Policies, tc.wantPolicy)
			}
		})
	}
}

func TestServerDeciderRemovedGroupMemberLosesAccess(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "decider.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	f := newDeciderFixture(t, data)
	dec := f.api.Decider(DecisionScope{OrgID: "org_a"})
	req := httpReq(user("usr_ops"), "ep_a")
	if d, _ := dec.Decide(context.Background(), req); !d.Allowed {
		t.Fatalf("before removal = %+v", d)
	}
	if err := data.RemoveGroupMember(context.Background(), "org_a", "grp_ops", store.GroupMemberUser, "usr_ops"); err != nil {
		t.Fatal(err)
	}
	if d, _ := dec.Decide(context.Background(), req); d.Allowed {
		t.Fatalf("after removal = %+v", d)
	}
}

type deciderFaultStore struct {
	store.DataStore
	failOn string
}

var errDeciderStore = errors.New("store down")

func (s *deciderFaultStore) fail(op string) bool { return s.failOn == op }

func (s *deciderFaultStore) GetOrgPolicy(ctx context.Context, org string) (*store.OrgPolicy, error) {
	if s.fail("policy") {
		return nil, errDeciderStore
	}
	return s.DataStore.GetOrgPolicy(ctx, org)
}

func (s *deciderFaultStore) GetMembership(ctx context.Context, org, user string) (*store.Membership, error) {
	if s.fail("membership") {
		return nil, errDeciderStore
	}
	return s.DataStore.GetMembership(ctx, org, user)
}

func (s *deciderFaultStore) ListMemberGroups(ctx context.Context, org, typ, id string) ([]*store.Group, error) {
	if s.fail("groups") {
		return nil, errDeciderStore
	}
	return s.DataStore.ListMemberGroups(ctx, org, typ, id)
}

func (s *deciderFaultStore) GetEndpoint(ctx context.Context, id string) (*store.Endpoint, error) {
	if s.fail("endpoint") {
		return nil, errDeciderStore
	}
	return s.DataStore.GetEndpoint(ctx, id)
}

func (s *deciderFaultStore) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	if s.fail("agent") {
		return nil, errDeciderStore
	}
	return s.DataStore.GetAgent(ctx, id)
}

func (s *deciderFaultStore) GetSession(ctx context.Context, hash string) (*store.Session, error) {
	if s.fail("session") {
		return nil, errDeciderStore
	}
	return s.DataStore.GetSession(ctx, hash)
}

func TestServerDeciderFailsClosedOnStoreErrors(t *testing.T) {
	inner, err := sqlite.Open(filepath.Join(t.TempDir(), "decider.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	faulty := &deciderFaultStore{DataStore: inner}
	f := newDeciderFixture(t, faulty)

	tests := []struct {
		failOn string
		scope  DecisionScope
		req    authz.DecisionRequest
	}{
		{"policy", DecisionScope{OrgID: "org_a"}, httpReq(user("usr_ops"), "ep_a")},
		{"membership", DecisionScope{OrgID: "org_a"}, httpReq(user("usr_ops"), "ep_a")},
		{"groups", DecisionScope{OrgID: "org_a"}, httpReq(user("usr_ops"), "ep_a")},
		{"endpoint", DecisionScope{OrgID: "org_a"}, httpReq(user("usr_ops"), "ep_a")},
		{"session", DecisionScope{OrgID: "org_a", SessionIDHash: "sess_live"}, httpReq(user("usr_ops"), "ep_a")},
		{"agent", DecisionScope{OrgID: "org_a", AgentID: "ag_a"}, connectReq("ag_a", "ep_a")},
	}
	for _, tc := range tests {
		t.Run(tc.failOn, func(t *testing.T) {
			faulty.failOn = tc.failOn
			f.api.invalidateAuthz("org_a")
			d, err := f.api.Decider(tc.scope).Decide(context.Background(), tc.req)
			if !errors.Is(err, errDeciderStore) {
				t.Fatalf("err = %v, want store error", err)
			}
			if d.Allowed || d.Reason != authz.ReasonUnavailable || d.TTLSeconds != 0 {
				t.Fatalf("decision = %+v, want unavailable deny", d)
			}
		})
	}
}
