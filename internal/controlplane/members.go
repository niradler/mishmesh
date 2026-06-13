package controlplane

import (
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func (a *API) listOrgsHandler(w http.ResponseWriter, r *http.Request) {
	if su, ok := a.resolveSession(r); ok {
		memberships, err := a.data.ListMembershipsByUser(r.Context(), su.user.ID)
		if a.handleErr(w, err) {
			return
		}
		out := make([]*store.Org, 0, len(memberships))
		for _, m := range memberships {
			if org, err := a.data.GetOrg(r.Context(), m.OrgID); err == nil {
				out = append(out, org)
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	orgs, err := a.data.ListOrgs(r.Context())
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, orgs)
}

type memberDTO struct {
	User      map[string]string `json:"user"`
	Role      string            `json:"role"`
	CreatedAt time.Time         `json:"created_at"`
}

func (a *API) listMembersHandler(w http.ResponseWriter, r *http.Request) {
	memberships, err := a.data.ListMembershipsByOrg(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	out := make([]memberDTO, 0, len(memberships))
	for _, m := range memberships {
		user, err := a.data.GetUserByID(r.Context(), m.UserID)
		if err != nil {
			continue
		}
		out = append(out, memberDTO{User: userDTO(user), Role: m.Role, CreatedAt: m.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) addMemberHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !validRole(req.Role) {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	user, err := a.data.GetUserByEmail(r.Context(), req.Email)
	if a.handleErr(w, err) {
		return
	}
	orgID := a.orgScope(r)
	if err := a.data.CreateMembership(r.Context(), &store.Membership{OrgID: orgID, UserID: user.ID, Role: req.Role, CreatedAt: time.Now()}); a.handleErr(w, err) {
		return
	}
	a.audit(r, "member.add", user.ID, req.Role)
	writeJSON(w, http.StatusCreated, memberDTO{User: userDTO(user), Role: req.Role, CreatedAt: time.Now()})
}

func (a *API) updateMemberHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !validRole(req.Role) {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	orgID := a.orgScope(r)
	userID := r.PathValue("user_id")
	if _, err := a.data.GetMembership(r.Context(), orgID, userID); a.handleErr(w, err) {
		return
	}
	if err := a.data.UpdateMembership(r.Context(), &store.Membership{OrgID: orgID, UserID: userID, Role: req.Role}); a.handleErr(w, err) {
		return
	}
	a.audit(r, "member.update", userID, req.Role)
	writeJSON(w, http.StatusOK, map[string]string{"user_id": userID, "role": req.Role})
}

func (a *API) removeMemberHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	userID := r.PathValue("user_id")
	if err := a.data.DeleteMembership(r.Context(), orgID, userID); a.handleErr(w, err) {
		return
	}
	a.audit(r, "member.remove", userID, "")
	w.WriteHeader(http.StatusNoContent)
}

func validRole(role string) bool {
	switch role {
	case store.RoleOwner, store.RoleAdmin, store.RoleMember:
		return true
	default:
		return false
	}
}
