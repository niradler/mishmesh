package authz

import (
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/types"
)

//go:embed policy.cedar
var defaultPolicy []byte

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

type Action string

const (
	ActionAgentRead     Action = "agent:read"
	ActionAgentWrite    Action = "agent:write"
	ActionEndpointRead  Action = "endpoint:read"
	ActionEndpointWrite Action = "endpoint:write"
	ActionQuotaRead     Action = "quota:read"
	ActionQuotaWrite    Action = "quota:write"
	ActionMemberRead    Action = "member:read"
	ActionMemberManage  Action = "member:manage"
	ActionAuditRead     Action = "audit:read"
	ActionStatusRead    Action = "status:read"
	ActionPolicyRead    Action = "policy:read"
	ActionPolicyWrite   Action = "policy:write"
)

func AllActions() []Action {
	return []Action{
		ActionAgentRead, ActionAgentWrite,
		ActionEndpointRead, ActionEndpointWrite,
		ActionQuotaRead, ActionQuotaWrite,
		ActionMemberRead, ActionMemberManage,
		ActionAuditRead, ActionStatusRead,
		ActionPolicyRead, ActionPolicyWrite,
	}
}

type Principal struct {
	ID   string
	Role string
	Org  string
}

func CompileMatrix(matrix map[string][]Action) string {
	roles := make([]string, 0, len(matrix))
	for role := range matrix {
		roles = append(roles, role)
	}
	sort.Strings(roles)

	var b strings.Builder
	for _, role := range roles {
		actions := dedupeSorted(matrix[role])
		if len(actions) == 0 {
			continue
		}
		quoted := make([]string, len(actions))
		for i, act := range actions {
			quoted[i] = fmt.Sprintf("Action::%q", string(act))
		}
		fmt.Fprintf(&b, "permit (\n  principal in Role::%q,\n  action in [%s],\n  resource\n);\n", role, strings.Join(quoted, ", "))
	}
	return b.String()
}

func ProbeMatrix(az *Authorizer, roles []string) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(roles))
	for _, role := range roles {
		row := make(map[string]bool, len(AllActions()))
		for _, act := range AllActions() {
			row[string(act)] = az.Authorize(Principal{ID: "probe", Role: role, Org: "probe"}, act)
		}
		out[role] = row
	}
	return out
}

func dedupeSorted(actions []Action) []Action {
	seen := make(map[Action]struct{}, len(actions))
	out := make([]Action, 0, len(actions))
	for _, a := range actions {
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

type Authorizer struct {
	policies *cedar.PolicySet
}

func DefaultPolicySource() []byte {
	src := make([]byte, len(defaultPolicy))
	copy(src, defaultPolicy)
	return src
}

func Default() *Authorizer {
	az, err := New(defaultPolicy)
	if err != nil {
		panic("authz: embedded default policy invalid: " + err.Error())
	}
	return az
}

func New(policySrc []byte) (*Authorizer, error) {
	ps, err := cedar.NewPolicySetFromBytes("policy.cedar", policySrc)
	if err != nil {
		return nil, fmt.Errorf("parse cedar policy: %w", err)
	}
	return &Authorizer{policies: ps}, nil
}

func (a *Authorizer) Authorize(p Principal, action Action) bool {
	principalUID := types.NewEntityUID("User", types.String(p.ID))
	roleUID := types.NewEntityUID("Role", types.String(p.Role))
	orgUID := types.NewEntityUID("Org", types.String(p.Org))

	entities := types.EntityMap{
		principalUID: types.Entity{UID: principalUID, Parents: types.NewEntityUIDSet(roleUID)},
		roleUID:      types.Entity{UID: roleUID},
		orgUID:       types.Entity{UID: orgUID},
	}

	req := cedar.Request{
		Principal: principalUID,
		Action:    types.NewEntityUID("Action", types.String(string(action))),
		Resource:  orgUID,
		Context:   types.NewRecord(nil),
	}
	decision, _ := a.policies.IsAuthorized(entities, req)
	return decision == cedar.Allow
}
