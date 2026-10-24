package authz

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"
)

var decideNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func userSubject(groups ...string) *Subject {
	refs := make([]GroupRef, len(groups))
	for i, g := range groups {
		refs[i] = GroupRef{ID: g, Name: g}
	}
	return &Subject{Ref: EntityRef{Type: EntityUser, ID: "usr_1"}, Email: "u@example.com", Role: RoleMember, Groups: refs}
}

func httpRequest(path string) DecisionRequest {
	return DecisionRequest{
		Principal: EntityRef{Type: EntityUser, ID: "usr_1"},
		Action:    ActionHTTPRequest,
		Resource:  EntityRef{Type: EntityEndpoint, ID: "ep_1"},
		Context:   RequestContext{ClientIP: "10.1.2.3", Host: "app.example.com", Method: "GET", Path: path, AuthMethod: AuthMethodPassword},
	}
}

func httpFacts(subject *Subject) Facts {
	return Facts{Now: decideNow, Subject: subject, Endpoint: &EndpointEntity{ID: "ep_1", Kind: "http", Method: "native"}}
}

func connectRequest() DecisionRequest {
	return DecisionRequest{
		Principal: EntityRef{Type: EntityAgent, ID: "ag_1"},
		Action:    ActionAgentConnect,
		Resource:  EntityRef{Type: EntityEndpoint, ID: "ep_1"},
		Context:   RequestContext{ClientIP: "10.1.2.3", Protocol: "tcp", AuthMethod: AuthMethodAgentToken},
	}
}

func connectFacts(endpoint EndpointEntity) Facts {
	agent := &AgentEntity{ID: "ag_1", Name: "laptop"}
	return Facts{
		Now:      decideNow,
		Subject:  &Subject{Ref: EntityRef{Type: EntityAgent, ID: "ag_1"}, Name: "laptop"},
		Agent:    agent,
		Endpoint: &endpoint,
	}
}

func mustAuthorizer(t *testing.T, src string) *Authorizer {
	t.Helper()
	if err := Validate([]byte(src)); err != nil {
		t.Fatalf("test policy invalid: %v", err)
	}
	az, err := New([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return az
}

func TestShouldApplyHardChecksBeforeCedar(t *testing.T) {
	allowAll := mustAuthorizer(t, `permit (principal, action, resource);`)
	expired := decideNow.Add(-time.Minute)
	tests := []struct {
		name   string
		mutate func(*DecisionRequest, *Facts)
		want   Reason
	}{
		{"baseline allows", func(*DecisionRequest, *Facts) {}, ReasonAllow},
		{"control plane action", func(r *DecisionRequest, _ *Facts) { r.Action = ActionAgentRead }, ReasonInvalidRequest},
		{"missing subject", func(_ *DecisionRequest, f *Facts) { f.Subject = nil }, ReasonUnknownEntity},
		{"subject mismatch", func(_ *DecisionRequest, f *Facts) { f.Subject.Ref.ID = "usr_other" }, ReasonUnknownEntity},
		{"missing endpoint", func(_ *DecisionRequest, f *Facts) { f.Endpoint = nil }, ReasonUnknownEntity},
		{"endpoint mismatch", func(_ *DecisionRequest, f *Facts) { f.Endpoint.ID = "ep_other" }, ReasonUnknownEntity},
		{"user disabled", func(_ *DecisionRequest, f *Facts) { f.Subject.Disabled = true }, ReasonUserDisabled},
		{"session revoked", func(_ *DecisionRequest, f *Facts) {
			f.Session = &SessionFacts{ExpiresAt: decideNow.Add(time.Hour), Revoked: true}
		}, ReasonSessionRevoked},
		{"session expired", func(_ *DecisionRequest, f *Facts) { f.Session = &SessionFacts{ExpiresAt: expired} }, ReasonSessionExpired},
		{"revoked wins over expired", func(_ *DecisionRequest, f *Facts) { f.Session = &SessionFacts{ExpiresAt: expired, Revoked: true} }, ReasonSessionRevoked},
		{"disabled wins over session", func(_ *DecisionRequest, f *Facts) {
			f.Subject.Disabled = true
			f.Session = &SessionFacts{ExpiresAt: expired}
		}, ReasonUserDisabled},
		{"invalid client ip", func(r *DecisionRequest, _ *Facts) { r.Context.ClientIP = "not-an-ip" }, ReasonInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httpRequest("/")
			facts := httpFacts(userSubject())
			tt.mutate(&req, &facts)
			got := allowAll.Decide(req, facts)
			if got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
			if got.Allowed != (tt.want == ReasonAllow) {
				t.Fatalf("allowed = %v for reason %q", got.Allowed, got.Reason)
			}
			if tt.want != ReasonAllow && got.TTLSeconds != 0 {
				t.Fatalf("hard-check deny must not be cached, ttl = %d", got.TTLSeconds)
			}
		})
	}
}

func TestShouldDenyDisabledAgentEvenWhenPolicyAllows(t *testing.T) {
	allowAll := mustAuthorizer(t, `permit (principal, action, resource);`)
	tests := []struct {
		name   string
		mutate func(*Facts)
		want   Reason
	}{
		{"active agent", func(*Facts) {}, ReasonAllow},
		{"disabled agent", func(f *Facts) { f.Agent.Disabled = true }, ReasonAgentDisabled},
		{"missing agent", func(f *Facts) { f.Agent = nil }, ReasonUnknownEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := connectFacts(EndpointEntity{ID: "ep_1", Kind: "tcp", Method: "native"})
			tt.mutate(&facts)
			if got := allowAll.Decide(connectRequest(), facts); got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestShouldDecideWithCedarOutcomes(t *testing.T) {
	src := `
@id("ops-api")
@credential("ops-token")
permit (principal in Group::"grp_ops", action == Action::"http:request", resource)
when { context.path like "/api/*" };

@id("no-admin")
@reason("blocked_path")
forbid (principal, action == Action::"http:request", resource)
when { context.path like "/admin*" };
`
	az := mustAuthorizer(t, src)
	tests := []struct {
		name       string
		groups     []string
		path       string
		want       Reason
		wantPolicy string
		wantAnno   map[string]string
	}{
		{"group member on allowed path", []string{"grp_ops"}, "/api/x", ReasonAllow, "ops-api", map[string]string{"credential": "ops-token"}},
		{"not in group", nil, "/api/x", ReasonNoPolicyMatch, "", nil},
		{"group member outside path", []string{"grp_ops"}, "/other", ReasonNoPolicyMatch, "", nil},
		{"forbid wins", []string{"grp_ops"}, "/admin", ReasonPolicyDeny, "no-admin", map[string]string{"reason": "blocked_path"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := az.Decide(httpRequest(tt.path), httpFacts(userSubject(tt.groups...)))
			if got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
			if tt.wantPolicy == "" {
				if len(got.Policies) != 0 {
					t.Fatalf("policies = %+v, want none", got.Policies)
				}
				return
			}
			if len(got.Policies) != 1 || got.Policies[0].ID != tt.wantPolicy {
				t.Fatalf("policies = %+v, want %s", got.Policies, tt.wantPolicy)
			}
			for k, v := range tt.wantAnno {
				if got.Policies[0].Annotations[k] != v {
					t.Fatalf("annotation %s = %q, want %q", k, got.Policies[0].Annotations[k], v)
				}
			}
			if _, ok := got.Policies[0].Annotations["id"]; ok {
				t.Fatal("id annotation should be reported as the policy ID, not repeated")
			}
		})
	}
}

func TestShouldReportOwnerAndVersion(t *testing.T) {
	az := mustAuthorizer(t, `permit (principal, action, resource);`)
	other := mustAuthorizer(t, `permit (principal, action == Action::"http:request", resource);`)
	got := az.WithOwner("org_1").Decide(httpRequest("/"), httpFacts(userSubject()))
	if got.Owner != "org_1" {
		t.Fatalf("owner = %q", got.Owner)
	}
	if got.PolicyVersion == "" || got.PolicyVersion == other.Version() {
		t.Fatalf("version = %q, other = %q", got.PolicyVersion, other.Version())
	}
	if Default().Decide(httpRequest("/"), httpFacts(userSubject())).Owner != OwnerDefault {
		t.Fatal("default authorizer must report the default owner")
	}
}

func TestShouldFailClosedOnEvaluationError(t *testing.T) {
	src := `forbid (principal, action == Action::"http:request", resource) when { context.now.offset(duration("9223372036854775807ms")) > context.now };
permit (principal, action == Action::"http:request", resource);`
	az, err := New([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := az.Decide(httpRequest("/"), httpFacts(userSubject()))
	if got.Allowed || got.Reason != ReasonPolicyError {
		t.Fatalf("got %+v, want policy_error deny", got)
	}
}

func TestShouldStripStaleAuthTimeBeforeEvaluation(t *testing.T) {
	src := `permit (principal, action == Action::"http:request", resource)
when { context has auth_time && context.auth_time > context.now.offset(duration("-24h")) };`
	az := mustAuthorizer(t, src)
	tests := []struct {
		name    string
		authAge time.Duration
		maxAge  time.Duration
		want    Reason
	}{
		{"fresh", time.Hour, 12 * time.Hour, ReasonAllow},
		{"older than max age", 13 * time.Hour, 12 * time.Hour, ReasonAuthStale},
		{"default max age", 13 * time.Hour, 0, ReasonAuthStale},
		{"custom longer max age", 13 * time.Hour, 20 * time.Hour, ReasonAllow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httpRequest("/")
			authTime := decideNow.Add(-tt.authAge)
			req.Context.AuthTime = &authTime
			facts := httpFacts(userSubject())
			facts.MaxAuthAge = tt.maxAge
			if got := az.Decide(req, facts); got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestShouldUseServerTimeNotRequestTime(t *testing.T) {
	before := mustAuthorizer(t, `permit (principal, action == Action::"http:request", resource) when { context.now < datetime("2026-01-01") };`)
	after := mustAuthorizer(t, `permit (principal, action == Action::"http:request", resource) when { context.now > datetime("2026-01-01") };`)
	raw := []byte(`{"principal":{"type":"User","id":"usr_1"},"action":"http:request","resource":{"type":"Endpoint","id":"ep_1"},"context":{"client_ip":"10.0.0.1","method":"GET","path":"/","auth_method":"password","now":"2020-01-01T00:00:00Z"}}`)
	var req DecisionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if got := before.Decide(req, httpFacts(userSubject())); got.Allowed {
		t.Fatal("request-supplied time must be ignored")
	}
	if got := after.Decide(req, httpFacts(userSubject())); !got.Allowed {
		t.Fatalf("server time not used: %s", got.Reason)
	}
}

func TestShouldReproduceAllowAgentsWithDefaultPolicy(t *testing.T) {
	az := Default()
	tests := []struct {
		name     string
		endpoint EndpointEntity
		want     bool
	}{
		{"any agent", EndpointEntity{ID: "ep_1", ConnectAnyAgent: true}, true},
		{"listed agent", EndpointEntity{ID: "ep_1", ConnectAgents: []string{"ag_2", "ag_1"}}, true},
		{"unlisted agent", EndpointEntity{ID: "ep_1", ConnectAgents: []string{"ag_2"}}, false},
		{"no connect policy", EndpointEntity{ID: "ep_1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := az.Decide(connectRequest(), connectFacts(tt.endpoint)); got.Allowed != tt.want {
				t.Fatalf("allowed = %v (%s), want %v", got.Allowed, got.Reason, tt.want)
			}
		})
	}
}

func TestShouldAllowEndpointTrafficAndDenyEgressWithDefaultPolicy(t *testing.T) {
	az := Default()
	anonymous := DecisionRequest{
		Principal: EntityRef{Type: EntityAnonymous, ID: AnonymousID},
		Action:    ActionEndpointAccess,
		Resource:  EntityRef{Type: EntityEndpoint, ID: "ep_1"},
		Context:   RequestContext{ClientIP: "203.0.113.9", Host: "app.example.com", AuthMethod: AuthMethodNone},
	}
	facts := httpFacts(&Subject{Ref: EntityRef{Type: EntityAnonymous, ID: AnonymousID}})
	if got := az.Decide(anonymous, facts); !got.Allowed {
		t.Fatalf("anonymous endpoint access denied: %s", got.Reason)
	}
	if got := az.Decide(httpRequest("/any"), httpFacts(userSubject())); !got.Allowed {
		t.Fatalf("http request denied: %s", got.Reason)
	}
	egress := DecisionRequest{
		Principal: EntityRef{Type: EntityAgent, ID: "ag_1"},
		Action:    ActionEgressConnect,
		Resource:  EntityRef{Type: EntityDestination, ID: "example.com:443"},
		Context:   RequestContext{ClientIP: "10.0.0.1", AuthMethod: AuthMethodAgentToken},
	}
	egressFacts := connectFacts(EndpointEntity{})
	egressFacts.Endpoint = nil
	if got := az.Decide(egress, egressFacts); got.Allowed || got.Reason != ReasonNoPolicyMatch {
		t.Fatalf("egress = %+v, want no_policy_match deny", got)
	}
}

func TestShouldDecideEgressOnDestinationAttributes(t *testing.T) {
	az := mustAuthorizer(t, `permit (principal, action == Action::"egress:connect", resource) when { resource.host like "*.example.com" && resource.port == 443 && context has sni && context.sni == resource.host };`)
	tests := []struct {
		name        string
		destination string
		sni         string
		want        Reason
	}{
		{"matching", "api.example.com:443", "api.example.com", ReasonAllow},
		{"wrong port", "api.example.com:80", "api.example.com", ReasonNoPolicyMatch},
		{"sni mismatch", "api.example.com:443", "evil.test", ReasonNoPolicyMatch},
		{"no sni", "api.example.com:443", "", ReasonNoPolicyMatch},
		{"malformed destination", "api.example.com", "", ReasonInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := DecisionRequest{
				Principal: EntityRef{Type: EntityAgent, ID: "ag_1"},
				Action:    ActionEgressConnect,
				Resource:  EntityRef{Type: EntityDestination, ID: tt.destination},
				Context:   RequestContext{ClientIP: "10.0.0.1", AuthMethod: AuthMethodAgentToken, SNI: tt.sni},
			}
			facts := connectFacts(EndpointEntity{})
			facts.Endpoint = nil
			if got := az.Decide(req, facts); got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestShouldDeriveCacheScopePerAction(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		action Action
		want   CacheScope
	}{
		{"default policy", string(DefaultPolicySource()), ActionHTTPRequest, CacheScope{}},
		{"path on http", `permit (principal, action == Action::"http:request", resource) when { context.path like "/a*" };`, ActionHTTPRequest, CacheScope{Path: true}},
		{"method on http", `permit (principal, action == Action::"http:request", resource) when { context.method == "GET" };`, ActionHTTPRequest, CacheScope{Method: true}},
		{"has path", `forbid (principal, action == Action::"http:request", resource) when { context has path };`, ActionHTTPRequest, CacheScope{Path: true}},
		{"path policy other action", `permit (principal, action == Action::"http:request", resource) when { context.path like "/a*" };`, ActionEndpointAccess, CacheScope{}},
		{"unconstrained action", `permit (principal, action, resource) when { context.method == "GET" };`, ActionAgentConnect, CacheScope{Method: true}},
		{"action list", `permit (principal, action in [Action::"endpoint:access", Action::"http:request"], resource) when { context.path == "/" };`, ActionHTTPRequest, CacheScope{Path: true}},
		{"bare context", `permit (principal, action == Action::"http:request", resource) when { context == {} };`, ActionHTTPRequest, CacheScope{Path: true, Method: true}},
		{"other attrs", `permit (principal, action == Action::"http:request", resource) when { context.host == "a" };`, ActionHTTPRequest, CacheScope{}},
		{"resolved ip on egress connect", `forbid (principal, action == Action::"egress:connect", resource) when { context has resolved_ip && context.resolved_ip.isLoopback() };`, ActionEgressConnect, CacheScope{ResolvedIP: true}},
		{"resolved ip ignored for other actions", `permit (principal, action, resource) when { context has resolved_ip };`, ActionHTTPRequest, CacheScope{}},
		{"bare context on egress connect", `permit (principal, action == Action::"egress:connect", resource) when { context == {} };`, ActionEgressConnect, CacheScope{Path: true, Method: true, ResolvedIP: true}},
		{"hostname only egress policy", `forbid (principal, action == Action::"egress:connect", resource) when { context has exit };`, ActionEgressConnect, CacheScope{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			az, err := New([]byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			if got := az.Scope(tt.action); got != tt.want {
				t.Fatalf("scope = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestShouldCarryScopeAndTTLOnDecision(t *testing.T) {
	az := mustAuthorizer(t, `permit (principal, action == Action::"http:request", resource) when { context.path like "/a*" };`)
	allowed := az.Decide(httpRequest("/a"), httpFacts(userSubject()))
	if !allowed.Scope.Path || allowed.TTLSeconds != DefaultDecisionTTLSeconds {
		t.Fatalf("allow = %+v", allowed)
	}
	denied := az.Decide(httpRequest("/b"), httpFacts(userSubject()))
	if denied.Allowed || !denied.Scope.Path || denied.TTLSeconds != DefaultDecisionTTLSeconds {
		t.Fatalf("deny = %+v", denied)
	}
}

func TestShouldExposeUserRoleAndGroupsToDataPlanePolicies(t *testing.T) {
	az := mustAuthorizer(t, `permit (principal in Role::"admin", action == Action::"endpoint:access", resource);
permit (principal, action == Action::"endpoint:access", resource) when { principal is User && principal.email like "*@corp.example" };`)
	tests := []struct {
		name    string
		subject *Subject
		want    bool
	}{
		{"admin role", &Subject{Ref: EntityRef{Type: EntityUser, ID: "usr_1"}, Email: "x@else.example", Role: RoleAdmin}, true},
		{"email domain", &Subject{Ref: EntityRef{Type: EntityUser, ID: "usr_1"}, Email: "x@corp.example", Role: RoleMember}, true},
		{"neither", &Subject{Ref: EntityRef{Type: EntityUser, ID: "usr_1"}, Email: "x@else.example", Role: RoleMember}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httpRequest("/")
			req.Action = ActionEndpointAccess
			if got := az.Decide(req, httpFacts(tt.subject)); got.Allowed != tt.want {
				t.Fatalf("allowed = %v (%s), want %v", got.Allowed, got.Reason, tt.want)
			}
		})
	}
}

func TestShouldListPoliciesSortedForDeterminism(t *testing.T) {
	az := mustAuthorizer(t, `@id("b") permit (principal, action == Action::"http:request", resource);
@id("a") permit (principal, action == Action::"http:request", resource);`)
	got := az.Decide(httpRequest("/"), httpFacts(userSubject()))
	ids := make([]string, len(got.Policies))
	for i, p := range got.Policies {
		ids[i] = p.ID
	}
	if !slices.Equal(ids, []string{"a", "b"}) {
		t.Fatalf("ids = %v", ids)
	}
}

func TestShouldDecideEgressOnExitAgent(t *testing.T) {
	az := mustAuthorizer(t, `permit (principal, action == Action::"egress:connect", resource) when { context has exit && context.exit.name == "office-exit" };`)
	tests := []struct {
		name string
		exit string
		fact *AgentEntity
		want Reason
	}{
		{"named exit", "ag_2", &AgentEntity{ID: "ag_2", Name: "office-exit"}, ReasonAllow},
		{"other exit", "ag_3", &AgentEntity{ID: "ag_3", Name: "home-exit"}, ReasonNoPolicyMatch},
		{"no exit", "", nil, ReasonNoPolicyMatch},
		{"unknown exit", "ag_9", nil, ReasonUnknownEntity},
		{"exit fact for another agent", "ag_2", &AgentEntity{ID: "ag_3", Name: "office-exit"}, ReasonUnknownEntity},
		{"disabled exit", "ag_2", &AgentEntity{ID: "ag_2", Name: "office-exit", Disabled: true}, ReasonAgentDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := DecisionRequest{
				Principal: EntityRef{Type: EntityAgent, ID: "ag_1"},
				Action:    ActionEgressConnect,
				Resource:  EntityRef{Type: EntityDestination, ID: "api.example.com:443"},
				Context:   RequestContext{ClientIP: "10.0.0.1", AuthMethod: AuthMethodAgentToken, Exit: tt.exit},
			}
			facts := connectFacts(EndpointEntity{})
			facts.Endpoint = nil
			facts.Exit = tt.fact
			if got := az.Decide(req, facts); got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestShouldRejectExitOutsideEgress(t *testing.T) {
	req := connectRequest()
	req.Context.Exit = "ag_2"
	if err := req.Validate(); err == nil {
		t.Fatal("exit on agent:connect validated")
	}
}

func TestShouldDecideEgressOnResolvedIP(t *testing.T) {
	az := mustAuthorizer(t, `permit (principal, action == Action::"egress:connect", resource);
forbid (principal, action == Action::"egress:connect", resource) when { context has resolved_ip && context.resolved_ip.isInRange(ip("10.20.0.0/16")) };`)
	tests := []struct {
		name     string
		resolved string
		want     Reason
	}{
		{"public answer", "93.184.216.34", ReasonAllow},
		{"forbidden range", "10.20.3.4", ReasonPolicyDeny},
		{"ipv6 answer", "2606:2800:220:1::1", ReasonAllow},
		{"no answer", "", ReasonAllow},
		{"not an ip", "api.example.com", ReasonInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := DecisionRequest{
				Principal: EntityRef{Type: EntityAgent, ID: "ag_1"},
				Action:    ActionEgressConnect,
				Resource:  EntityRef{Type: EntityDestination, ID: "api.example.com:443"},
				Context:   RequestContext{ClientIP: "10.0.0.1", AuthMethod: AuthMethodAgentToken, ResolvedIP: tt.resolved},
			}
			facts := connectFacts(EndpointEntity{})
			facts.Endpoint = nil
			if got := az.Decide(req, facts); got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
		})
	}
}

func TestShouldRejectResolvedIPOutsideEgress(t *testing.T) {
	req := connectRequest()
	req.Context.ResolvedIP = "10.0.0.1"
	if err := req.Validate(); err == nil {
		t.Fatal("resolved_ip on agent:connect validated")
	}
}

func egressRequest(method, path string) DecisionRequest {
	return DecisionRequest{
		Principal: EntityRef{Type: EntityAgent, ID: "ag_1"},
		Action:    ActionEgressRequest,
		Resource:  EntityRef{Type: EntityDestination, ID: "api.example.com:443"},
		Context:   RequestContext{ClientIP: "10.0.0.1", AuthMethod: AuthMethodAgentToken, Method: method, Path: path, TLS: true},
	}
}

func egressFacts() Facts {
	facts := connectFacts(EndpointEntity{})
	facts.Endpoint = nil
	return facts
}

func TestShouldDenyEgressRequestWithDefaultPolicy(t *testing.T) {
	if got := Default().Decide(egressRequest(http.MethodGet, "/"), egressFacts()); got.Allowed || got.Reason != ReasonNoPolicyMatch {
		t.Fatalf("egress request = %+v, want no_policy_match deny", got)
	}
}

func TestShouldDecideEgressRequestOnMethodPathAndMode(t *testing.T) {
	az := mustAuthorizer(t, `@credential("billing")
permit (principal, action == Action::"egress:request", resource)
when { resource.host == "api.example.com" && context.method == "GET" && context.path like "/v1/*" && context.tls && !context.decrypted };`)
	tests := []struct {
		name      string
		method    string
		path      string
		decrypted bool
		want      Reason
	}{
		{"matching", http.MethodGet, "/v1/items", false, ReasonAllow},
		{"wrong method", http.MethodPost, "/v1/items", false, ReasonNoPolicyMatch},
		{"wrong path", http.MethodGet, "/admin", false, ReasonNoPolicyMatch},
		{"decrypted mode", http.MethodGet, "/v1/items", true, ReasonNoPolicyMatch},
		{"missing path", http.MethodGet, "", false, ReasonInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := egressRequest(tt.method, tt.path)
			req.Context.Decrypted = tt.decrypted
			got := az.Decide(req, egressFacts())
			if got.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.want)
			}
			if got.Allowed {
				if name, err := SelectedCredential(got); err != nil || name != "billing" {
					t.Fatalf("credential = %q, %v", name, err)
				}
			}
		})
	}
	if scope := az.Scope(ActionEgressRequest); scope != (CacheScope{Path: true, Method: true}) {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestShouldRejectDecryptedOutsideEgressRequest(t *testing.T) {
	req := httpRequest("/")
	req.Context.Decrypted = true
	if err := req.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("validate = %v", err)
	}
}

func TestShouldReportConflictingCredentials(t *testing.T) {
	d := Decision{Policies: []DeterminingPolicy{
		{ID: "a", Annotations: map[string]string{CredentialAnnotation: "x"}},
		{ID: "b", Annotations: map[string]string{CredentialAnnotation: "y"}},
	}}
	if _, err := SelectedCredential(d); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("err = %v", err)
	}
}
