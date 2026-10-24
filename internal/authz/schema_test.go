package authz

import (
	"strings"
	"testing"
)

func TestShouldValidatePolicyAgainstSchema(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr string
	}{
		{"default policy", string(DefaultPolicySource()), ""},
		{"compiled matrix", string(CompileOrgPolicy(map[string][]Action{RoleMember: {ActionAgentRead}})), ""},
		{"empty policy", "", ""},
		{"group scoped http request", `permit (principal in Group::"grp_1", action == Action::"http:request", resource) when { context.path like "/api/*" && context.method == "GET" };`, ""},
		{"ip range on access", `permit (principal, action == Action::"endpoint:access", resource) when { context.client_ip.isInRange(ip("10.0.0.0/8")) };`, ""},
		{"guarded auth time", `forbid (principal, action == Action::"http:request", resource) unless { context has auth_time && context.auth_time > context.now.offset(duration("-1h")) };`, ""},
		{"egress destination", `permit (principal, action == Action::"egress:connect", resource) when { resource.host like "*.example.com" && resource.port == 443 };`, ""},
		{"path on agent connect", `permit (principal, action == Action::"agent:connect", resource) when { context.path == "/x" };`, "path"},
		{"method on egress", `permit (principal, action == Action::"egress:connect", resource) when { context.method == "GET" };`, "method"},
		{"unguarded optional attribute", `forbid (principal, action == Action::"http:request", resource) unless { context.auth_time > datetime("2026-01-01") };`, "auth_time"},
		{"unknown action", `permit (principal, action == Action::"cloudflare:manage", resource);`, "cloudflare:manage"},
		{"unknown entity attribute", `permit (principal, action == Action::"endpoint:access", resource) when { resource.owner == "x" };`, "owner"},
		{"syntax error", `permit (`, "parse"},
		{"oversize", strings.Repeat(" ", MaxPolicyBytes+1), "too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate([]byte(tt.src))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestShouldAppendDataPlaneDefaultsOnlyToControlPlaneOnlyPolicies(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		wantAppend bool
	}{
		{"legacy matrix", CompileMatrix(map[string][]Action{RoleOwner: AllActions()}), true},
		{"empty", "", true},
		{"unknown legacy action", `permit (principal in Role::"owner", action == Action::"cloudflare:manage", resource);`, true},
		{"explicit data plane rule", `permit (principal, action == Action::"agent:connect", resource);`, false},
		{"data plane in action list", `forbid (principal, action in [Action::"agent:read", Action::"http:request"], resource);`, false},
		{"unconstrained action", `permit (principal in Role::"owner", action, resource);`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(ForOrgSource([]byte(tt.src)))
			appended := strings.Contains(got, "default-agent-connect")
			if appended != tt.wantAppend {
				t.Fatalf("appended = %v, want %v\n%s", appended, tt.wantAppend, got)
			}
			if !strings.HasPrefix(got, tt.src) {
				t.Fatalf("org source was rewritten:\n%s", got)
			}
		})
	}
}

func TestShouldKeepControlPlaneDecisionsWhenDataPlaneDefaultsAppended(t *testing.T) {
	src := CompileMatrix(map[string][]Action{RoleMember: {ActionAgentRead}})
	az, err := New(ForOrgSource([]byte(src)))
	if err != nil {
		t.Fatal(err)
	}
	member := Principal{ID: "m", Role: RoleMember, Org: "org_1"}
	if !az.Authorize(member, ActionAgentRead) || az.Authorize(member, ActionAgentWrite) {
		t.Fatal("control-plane matrix changed by data-plane defaults")
	}
}
