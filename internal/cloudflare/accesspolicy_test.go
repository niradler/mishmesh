package cloudflare

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
)

func (f *ownedFixture) trySync(t *testing.T) (connections.Status, error) {
	t.Helper()
	record, err := f.data.GetConnection(context.Background(), f.config.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := f.config
	config.Record = *record
	instance, err := f.open.Open(config, connections.Deps{Data: f.data, Origin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	return instance.Sync(context.Background())
}

func (f *ownedFixture) setPolicy(policy *store.AccessPolicy) {
	f.data.endpoints[0].Policy.AccessPolicy = policy
}

func (f *fakeCF) onlyOwn(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ownApps) != 1 || len(f.policies) != 1 {
		t.Fatalf("want one own app and policy, got %d apps %d policies", len(f.ownApps), len(f.policies))
	}
	var app, policy map[string]any
	for _, value := range f.ownApps {
		app = value
	}
	for _, value := range f.policies {
		policy = value
	}
	return app, policy
}

func ruleKinds(t *testing.T, object map[string]any, field string) []string {
	t.Helper()
	rules, _ := object[field].([]any)
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		kind := accessRuleKind(rule)
		body, _ := rule.(map[string]any)[kind].(map[string]any)
		for _, value := range body {
			kind += ":" + value.(string)
		}
		out = append(out, kind)
	}
	slices.Sort(out)
	return out
}

func TestShouldCreateOwnAccessAppAndPolicyForEndpointPolicy(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.setPolicy(&store.AccessPolicy{AllowEmails: []string{"ops@example.com"}, AllowEmailDomains: []string{"example.com"}, AllowGroups: []string{"grp1"}, RequireMFA: true, SessionDuration: "8h"})
	host := genericHost(t, fixture.sync(t), "a.example.com")
	if host.State != StatePublished || host.AccessApp != AccessAppOwn || host.AccessPolicy != AccessPolicyOK {
		t.Fatalf("host = %+v", host)
	}
	app, policy := fixture.fake.onlyOwn(t)
	if app["name"] != "mishmesh a.example.com" || app["domain"] != "a.example.com" || app["session_duration"] != "8h" || !accessAppLinks(AccessApp(app), policy["id"].(string)) {
		t.Fatalf("app = %+v", app)
	}
	if got := ruleKinds(t, policy, "include"); !slices.Equal(got, []string{"email:ops@example.com", "email_domain:example.com", "group:grp1"}) {
		t.Fatalf("include = %v", got)
	}
	if got := ruleKinds(t, policy, "require"); !slices.Equal(got, []string{"auth_method:mfa"}) {
		t.Fatalf("require = %v", got)
	}
	if accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
		t.Fatal("policy host must not be added to the shared app")
	}
	if !strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "a.example.com|") {
		t.Fatalf("ingress = %v", fixture.fake.ingressServices())
	}
}

func TestShouldRestoreManagedAccessFieldsAndKeepUnmanagedEdits(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.setPolicy(&store.AccessPolicy{AllowEmails: []string{"ops@example.com"}})
	fixture.sync(t)
	app, policy := fixture.fake.onlyOwn(t)
	fixture.fake.mu.Lock()
	policy["include"] = []any{map[string]any{"email": map[string]any{"email": "intruder@example.com"}}, map[string]any{"ip": map[string]any{"ip": "10.0.0.0/8"}}}
	policy["require"] = []any{map[string]any{"auth_method": map[string]any{"auth_method": "mfa"}}}
	policy["exclude"] = []any{map[string]any{"geo": map[string]any{"country_code": "XX"}}}
	app["name"] = "renamed"
	app["app_launcher_visible"] = false
	fixture.fake.mu.Unlock()

	host := genericHost(t, fixture.sync(t), "a.example.com")
	if host.AccessPolicy != AccessPolicyDriftFixed {
		t.Fatalf("host = %+v", host)
	}
	app, policy = fixture.fake.onlyOwn(t)
	if got := ruleKinds(t, policy, "include"); !slices.Equal(got, []string{"email:ops@example.com", "ip:10.0.0.0/8"}) {
		t.Fatalf("include = %v", got)
	}
	if got := ruleKinds(t, policy, "require"); len(got) != 0 {
		t.Fatalf("require = %v", got)
	}
	if got := ruleKinds(t, policy, "exclude"); !slices.Equal(got, []string{"geo:XX"}) {
		t.Fatalf("exclude = %v", got)
	}
	if app["name"] != "mishmesh a.example.com" || app["app_launcher_visible"] != false {
		t.Fatalf("app = %+v", app)
	}

	puts := fixture.fake.policyPuts + fixture.fake.ownAppPuts
	host = genericHost(t, fixture.sync(t), "a.example.com")
	if host.AccessPolicy != AccessPolicyOK || fixture.fake.policyPuts+fixture.fake.ownAppPuts != puts {
		t.Fatalf("steady state rewrote Access: host %+v puts %d -> %d", host, puts, fixture.fake.policyPuts+fixture.fake.ownAppPuts)
	}
}

func TestShouldRecreateOwnAccessAppDeletedInCloudflare(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.setPolicy(&store.AccessPolicy{AllowEmailDomains: []string{"example.com"}})
	fixture.sync(t)
	fixture.fake.mu.Lock()
	clear(fixture.fake.ownApps)
	fixture.fake.mu.Unlock()
	host := genericHost(t, fixture.sync(t), "a.example.com")
	if host.AccessPolicy != AccessPolicyDriftFixed {
		t.Fatalf("host = %+v", host)
	}
	fixture.fake.onlyOwn(t)
}

func TestShouldMoveHostBetweenSharedAndOwnAccessApps(t *testing.T) {
	fixture := newOwnedFixture(t)
	host := genericHost(t, fixture.sync(t), "a.example.com")
	if host.AccessApp != AccessAppShared || !accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
		t.Fatalf("shared start: %+v", host)
	}
	fixture.data.endpoints[0].Policy.Access[0].Policy = &store.AccessPolicy{AllowEmails: []string{"ops@example.com"}}
	host = genericHost(t, fixture.sync(t), "a.example.com")
	if host.AccessApp != AccessAppOwn || accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
		t.Fatalf("own: %+v shared app %+v", host, fixture.fake.app)
	}
	fixture.fake.onlyOwn(t)
	fixture.data.endpoints[0].Policy.Access[0].Policy = nil
	host = genericHost(t, fixture.sync(t), "a.example.com")
	if host.AccessApp != AccessAppShared || !accessHasHost(AccessApp(fixture.fake.app), "a.example.com") || len(fixture.fake.ownApps) != 0 || len(fixture.fake.policies) != 0 {
		t.Fatalf("back to shared: %+v apps %d policies %d", host, len(fixture.fake.ownApps), len(fixture.fake.policies))
	}
}

func TestShouldNotRouteHostWhenOwnAccessAppCannotBeCreated(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.setPolicy(&store.AccessPolicy{AllowEmails: []string{"ops@example.com"}})
	fixture.fake.failAppCreate = true
	status, err := fixture.trySync(t)
	if err == nil {
		t.Fatal("want sync error")
	}
	host := genericHost(t, status, "a.example.com")
	if host.State != StateError || host.AccessPolicy != AccessPolicyError {
		t.Fatalf("host = %+v", host)
	}
	if strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "a.example.com|") || len(fixture.fake.records["z-example"]) != 0 {
		t.Fatalf("unprotected host was published: ingress %v dns %v", fixture.fake.ingressServices(), fixture.fake.records)
	}
	fixture.fake.failAppCreate = false
	host = genericHost(t, fixture.sync(t), "a.example.com")
	if host.State != StatePublished || host.AccessPolicy != AccessPolicyOK {
		t.Fatalf("retry host = %+v", host)
	}
	fixture.fake.onlyOwn(t)
}

func TestShouldPruneOwnAccessAppWithOrphanedHost(t *testing.T) {
	fixture := newOwnedFixture(t)
	fixture.setPolicy(&store.AccessPolicy{AllowEmails: []string{"ops@example.com"}})
	fixture.sync(t)
	fixture.fake.onlyOwn(t)
	fixture.data.endpoints[0].Policy.Access = nil
	fixture.data.endpoints[0].Policy.AccessPolicy = nil
	fixture.sync(t)
	if len(fixture.fake.ownApps) != 0 || len(fixture.fake.policies) != 0 || strings.Contains(strings.Join(fixture.fake.ingressServices(), ","), "a.example.com|") {
		t.Fatalf("orphan not pruned: apps %d policies %d ingress %v", len(fixture.fake.ownApps), len(fixture.fake.policies), fixture.fake.ingressServices())
	}
	record, err := fixture.data.GetConnection(context.Background(), "a")
	if err != nil || strings.Contains(string(record.State), "a.example.com") {
		t.Fatalf("ownership kept pruned host: %s %v", record.State, err)
	}
}

func TestShouldRejectAccessPolicyWithoutAPIToken(t *testing.T) {
	provider := NewProvider(ProviderOptions{}).(connections.AccessPolicyProvider)
	if err := provider.ValidateAccessPolicy(providerConfig(t, "a", "127.0.0.1:20241", false)); err == nil || !strings.Contains(err.Error(), "API token") {
		t.Fatalf("err = %v", err)
	}
	if err := provider.ValidateAccessPolicy(providerConfig(t, "a", "127.0.0.1:20241", true)); err != nil {
		t.Fatalf("managed err = %v", err)
	}
}
