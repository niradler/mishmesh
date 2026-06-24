package controlplane

import (
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

var policyRoles = []string{authz.RoleOwner, authz.RoleAdmin, authz.RoleMember}

type policyDTO struct {
	IsDefault bool                       `json:"is_default"`
	Roles     []string                   `json:"roles"`
	Actions   []string                   `json:"actions"`
	Matrix    map[string]map[string]bool `json:"matrix"`
	CedarSrc  string                     `json:"cedar_src"`
}

func actionStrings() []string {
	acts := authz.AllActions()
	out := make([]string, len(acts))
	for i, a := range acts {
		out[i] = string(a)
	}
	return out
}

func (a *API) getPolicyHandler(w http.ResponseWriter, r *http.Request) {
	org := a.orgScope(r)
	az := a.authorizerFor(r.Context(), org)
	isDefault := false
	cedarSrc := ""
	if pol, err := a.data.GetOrgPolicy(r.Context(), org); err == nil && pol.CedarSrc != "" {
		cedarSrc = pol.CedarSrc
	} else {
		isDefault = true
		cedarSrc = string(authz.DefaultPolicySource())
	}
	writeJSON(w, http.StatusOK, policyDTO{
		IsDefault: isDefault,
		Roles:     policyRoles,
		Actions:   actionStrings(),
		Matrix:    authz.ProbeMatrix(az, policyRoles),
		CedarSrc:  cedarSrc,
	})
}

func (a *API) putPolicyHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Matrix map[string][]string `json:"matrix"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	matrix, ok := normalizeMatrix(w, req.Matrix)
	if !ok {
		return
	}
	src := authz.CompileMatrix(matrix)
	if _, err := authz.New([]byte(src)); err != nil {
		writeError(w, http.StatusBadRequest, "policy compilation failed")
		return
	}
	org := a.orgScope(r)
	if err := a.data.SetOrgPolicy(r.Context(), &store.OrgPolicy{OrgID: org, CedarSrc: src, UpdatedAt: time.Now()}); err != nil {
		a.handleErr(w, err)
		return
	}
	a.invalidateAuthz(org)
	a.audit(r, "policy.update", org, "")
	a.getPolicyHandler(w, r)
}

func normalizeMatrix(w http.ResponseWriter, raw map[string][]string) (map[string][]authz.Action, bool) {
	valid := make(map[string]struct{}, len(authz.AllActions()))
	for _, act := range authz.AllActions() {
		valid[string(act)] = struct{}{}
	}
	validRole := map[string]struct{}{authz.RoleOwner: {}, authz.RoleAdmin: {}, authz.RoleMember: {}}

	out := make(map[string][]authz.Action, len(raw))
	for role, acts := range raw {
		if _, ok := validRole[role]; !ok {
			writeError(w, http.StatusBadRequest, "unknown role: "+role)
			return nil, false
		}
		converted := make([]authz.Action, 0, len(acts))
		for _, act := range acts {
			if _, ok := valid[act]; !ok {
				writeError(w, http.StatusBadRequest, "unknown action: "+act)
				return nil, false
			}
			converted = append(converted, authz.Action(act))
		}
		out[role] = converted
	}
	return out, true
}
