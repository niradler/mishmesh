package authz

import "testing"

func TestDefaultPolicyRoleMatrix(t *testing.T) {
	az := Default()

	writes := []Action{ActionAgentWrite, ActionEndpointWrite, ActionQuotaWrite, ActionMemberManage, ActionPolicyWrite}
	reads := []Action{ActionAgentRead, ActionEndpointRead, ActionQuotaRead, ActionMemberRead, ActionAuditRead, ActionStatusRead, ActionPolicyRead}

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
