package authz

import "testing"

func TestShouldReplaceCloudflareActionWithoutRewritingCustomPolicy(t *testing.T) {
	for _, action := range AllActions() {
		if action == Action("cloudflare:manage") {
			t.Fatal("old action remains registered")
		}
	}
	owner := Principal{ID: "owner", Role: RoleOwner, Org: "org"}
	if Default().Authorize(owner, Action("cloudflare:manage")) {
		t.Fatal("default policy accepts removed action")
	}
	source := []byte(`permit (principal in Role::"owner", action == Action::"cloudflare:manage", resource);`)
	custom, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	if custom.Authorize(owner, ActionConnectionManage) || !custom.Authorize(owner, Action("cloudflare:manage")) {
		t.Fatal("custom Cedar policy was rewritten implicitly")
	}
}

func TestDefaultPolicyRoleMatrix(t *testing.T) {
	az := Default()

	writes := []Action{ActionAgentWrite, ActionEndpointWrite, ActionQuotaWrite, ActionMemberManage, ActionPolicyWrite, ActionServiceWrite, ActionConnectionManage}
	reads := []Action{ActionAgentRead, ActionEndpointRead, ActionQuotaRead, ActionMemberRead, ActionAuditRead, ActionStatusRead, ActionPolicyRead, ActionServiceRead}

	for _, role := range []string{RoleOwner, RoleAdmin} {
		p := Principal{ID: "u@example.com", Role: role, Org: "org_1"}
		for _, act := range append(append([]Action{}, writes...), reads...) {
			if !az.Authorize(p, act) {
				t.Errorf("%s should be allowed %s", role, act)
			}
		}
	}

	member := Principal{ID: "m@example.com", Role: RoleMember, Org: "org_1"}
	for _, act := range reads {
		if !az.Authorize(member, act) {
			t.Errorf("member should be allowed read %s", act)
		}
	}
	for _, act := range writes {
		if az.Authorize(member, act) {
			t.Errorf("member should be denied write %s", act)
		}
	}
}

func TestHostExecIsOwnerOnlyByDefault(t *testing.T) {
	az := Default()
	tests := []struct {
		role string
		want bool
	}{
		{RoleOwner, true},
		{RoleAdmin, false},
		{RoleMember, false},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := az.Authorize(Principal{ID: "u", Role: tt.role, Org: "org_1"}, ActionHostExec)
			if got != tt.want {
				t.Errorf("host:exec for %s = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

func TestUnknownRoleDeniedByDefault(t *testing.T) {
	az := Default()
	p := Principal{ID: "x@example.com", Role: "guest", Org: "org_1"}
	if az.Authorize(p, ActionAgentRead) {
		t.Error("unknown role should be denied")
	}
}

func TestCompileMatrixRoundTrip(t *testing.T) {
	matrix := map[string][]Action{
		RoleOwner:  AllActions(),
		RoleAdmin:  AllActions(),
		RoleMember: {ActionAgentRead, ActionEndpointRead},
	}
	src := CompileMatrix(matrix)
	az, err := New([]byte(src))
	if err != nil {
		t.Fatalf("compiled policy did not parse: %v\n%s", err, src)
	}
	member := Principal{ID: "m@example.com", Role: RoleMember, Org: "org_1"}
	if !az.Authorize(member, ActionAgentRead) {
		t.Error("member should have agent:read")
	}
	if az.Authorize(member, ActionQuotaRead) {
		t.Error("member should not have quota:read (not in matrix)")
	}
	owner := Principal{ID: "o@example.com", Role: RoleOwner, Org: "org_1"}
	if !az.Authorize(owner, ActionPolicyWrite) {
		t.Error("owner should have policy:write")
	}
}

func TestCompileMatrixDeterministic(t *testing.T) {
	m := map[string][]Action{RoleMember: {ActionEndpointRead, ActionAgentRead}}
	first := CompileMatrix(m)
	for range 5 {
		if CompileMatrix(m) != first {
			t.Error("CompileMatrix must be deterministic")
		}
	}
}

func TestCompileEmptyMatrixDeniesAll(t *testing.T) {
	az, err := New([]byte(CompileMatrix(map[string][]Action{})))
	if err != nil {
		t.Fatalf("empty matrix should yield valid (deny-all) policy: %v", err)
	}
	if az.Authorize(Principal{ID: "x", Role: RoleOwner, Org: "o"}, ActionAgentRead) {
		t.Error("empty matrix should deny everything")
	}
}

func TestInvalidPolicyRejected(t *testing.T) {
	if _, err := New([]byte("this is not cedar")); err == nil {
		t.Error("expected parse error for invalid policy")
	}
}

func TestCustomPolicyOverridesDefault(t *testing.T) {
	src := []byte(`permit (principal in Role::"member", action == Action::"agent:write", resource);`)
	az, err := New(src)
	if err != nil {
		t.Fatalf("New(custom): %v", err)
	}
	member := Principal{ID: "m@example.com", Role: RoleMember, Org: "org_1"}
	if !az.Authorize(member, ActionAgentWrite) {
		t.Error("custom policy should allow member agent:write")
	}
	if az.Authorize(member, ActionAgentRead) {
		t.Error("custom policy grants only agent:write, not agent:read")
	}
}
