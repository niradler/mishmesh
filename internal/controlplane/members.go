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
		out := make([]orgDTO, 0, len(memberships))
		for _, m := range memberships {
			if org, err := a.data.GetOrg(r.Context(), m.OrgID); err == nil {
				out = append(out, toOrgDTO(org))
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	orgs, err := a.data.ListOrgs(r.Context())
	if a.handleErr(w, err) {
		return
	}
	out := make([]orgDTO, 0, len(orgs))
	for _, org := range orgs {
		out = append(out, toOrgDTO(org))
	}
	writeJSON(w, http.StatusOK, out)
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
	email := normalizeEmail(req.Email)
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "valid email required")
		return
	}
	if !a.canGrant(r, req.Role) {
		writeError(w, http.StatusForbidden, "cannot grant a role above your own")
		return
	}
	actor, _ := r.Context().Value(ctxActor).(string)
	inv, token, err := a.createInvite(r.Context(), a.orgScope(r), email, req.Role, actor)
	if err != nil {
		a.log.Warn("create invite failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(r, "invite.create", inv.ID, req.Role)
	dto := toInviteDTO(inv)
	dto.InviteToken = token
	dto.InviteURL = inviteURL(token)
	writeJSON(w, http.StatusCreated, dto)
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
	if !a.canGrant(r, req.Role) {
		writeError(w, http.StatusForbidden, "cannot grant a role above your own")
		return
	}
	orgID := a.orgScope(r)
	userID := r.PathValue("user_id")
	if !a.authorizeMemberChange(w, r, orgID, userID, req.Role) {
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
	if !a.authorizeMemberChange(w, r, orgID, userID, "") {
		return
	}
	if err := a.data.DeleteMembership(r.Context(), orgID, userID); a.handleErr(w, err) {
		return
	}
	a.audit(r, "member.remove", userID, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) authorizeMemberChange(w http.ResponseWriter, r *http.Request, orgID, userID, newRole string) bool {
	target, err := a.data.GetMembership(r.Context(), orgID, userID)
	if a.handleErr(w, err) {
		return false
	}
	if roleRank(target.Role) > roleRank(a.callerRole(r)) {
		writeError(w, http.StatusForbidden, "cannot modify a member above your own role")
		return false
	}
	if target.Role != store.RoleOwner || newRole == store.RoleOwner {
		return true
	}
	owners, err := a.countOwners(r, orgID)
	if a.handleErr(w, err) {
		return false
	}
	if owners <= 1 {
		writeError(w, http.StatusConflict, "an organization must keep at least one owner")
		return false
	}
	return true
}

func (a *API) countOwners(r *http.Request, orgID string) (int, error) {
	ms, err := a.data.ListMembershipsByOrg(r.Context(), orgID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ms {
		if m.Role == store.RoleOwner {
			n++
		}
	}
	return n, nil
}

func validRole(role string) bool {
	switch role {
	case store.RoleOwner, store.RoleAdmin, store.RoleMember:
		return true
	default:
		return false
	}
}
