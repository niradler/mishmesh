package controlplane

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
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
	writeList(a, w, orgs, err, toOrgDTO)
}

type memberDTO struct {
	User      map[string]string `json:"user"`
	Role      string            `json:"role"`
	Disabled  bool              `json:"disabled"`
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
		out = append(out, memberDTO{User: userDTO(user), Role: m.Role, Disabled: m.DisabledAt != nil, CreatedAt: m.CreatedAt})
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
	inv, token, err := a.createInvite(r.Context(), a.orgScope(r), email, req.Role, a.actor(r))
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
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role == nil && req.Disabled == nil {
		writeError(w, http.StatusBadRequest, "role or disabled required")
		return
	}
	orgID := a.orgScope(r)
	userID := r.PathValue("user_id")
	if req.Disabled != nil && *req.Disabled {
		defer a.memberMutations.lock(orgID)()
	}
	target, err := a.data.GetMembership(r.Context(), orgID, userID)
	if a.handleErr(w, err) {
		return
	}
	role := target.Role
	if req.Role != nil {
		if !validRole(*req.Role) {
			writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
		if !a.canGrant(r, *req.Role) {
			writeError(w, http.StatusForbidden, "cannot grant a role above your own")
			return
		}
		role = *req.Role
	}
	disabling := req.Disabled != nil && *req.Disabled && target.DisabledAt == nil
	if disabling && a.isSelf(r, userID) {
		writeError(w, http.StatusConflict, "cannot disable your own membership")
		return
	}
	if !a.authorizeMemberChange(w, r, orgID, userID, disabling) {
		return
	}
	if req.Role != nil && role != target.Role {
		if a.handleMemberChangeErr(w, a.data.DemoteOrRemoveMembershipKeepingOwner(r.Context(), orgID, userID, role, false)) {
			return
		}
		a.flushDecisions(orgID)
		a.audit(r, "member.update", userID, role)
	}
	disabled := target.DisabledAt != nil
	if req.Disabled != nil && *req.Disabled != disabled {
		var at *time.Time
		if *req.Disabled {
			now := a.now().UTC()
			at = &now
		}
		if err := a.data.SetMembershipDisabled(r.Context(), orgID, userID, at); a.handleErr(w, err) {
			return
		}
		disabled = *req.Disabled
		if disabled {
			if err := a.revokeMemberAccess(r.Context(), orgID, userID); err != nil {
				a.log.Error("revoke disabled member sessions failed", "org", orgID, "user", userID, "err", err)
				writeStoreUnavailable(w)
				return
			}
			a.audit(r, "member.disable", userID, "")
		} else {
			a.audit(r, "member.enable", userID, "")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "role": role, "disabled": disabled})
}

func (a *API) removeMemberHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	userID := r.PathValue("user_id")
	if !a.authorizeMemberChange(w, r, orgID, userID, false) {
		return
	}
	if a.handleMemberChangeErr(w, a.data.DemoteOrRemoveMembershipKeepingOwner(r.Context(), orgID, userID, "", true)) {
		return
	}
	if err := a.revokeMemberAccess(r.Context(), orgID, userID); err != nil {
		a.log.Error("revoke removed member sessions failed", "org", orgID, "user", userID, "err", err)
		writeStoreUnavailable(w)
		return
	}
	a.audit(r, "member.remove", userID, "")
	w.WriteHeader(http.StatusNoContent)
}

type orgLocks struct {
	locks sync.Map
}

func (l *orgLocks) lock(orgID string) func() {
	m, _ := l.locks.LoadOrStore(orgID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (a *API) revokeMemberAccess(ctx context.Context, orgID, userID string) error {
	var errs []error
	if err := a.data.DeleteUserSessions(ctx, orgID, userID); err != nil {
		errs = append(errs, err)
	}
	if a.identity != nil {
		if err := a.identity.RevokeSubject(ctx, orgID, authz.EntityRef{Type: authz.EntityUser, ID: userID}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *API) isSelf(r *http.Request, userID string) bool {
	user, err := a.data.GetUserByID(r.Context(), userID)
	return err == nil && user.Email == a.actor(r)
}

func (a *API) handleMemberChangeErr(w http.ResponseWriter, err error) bool {
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "an organization must keep at least one owner")
		return true
	}
	return a.handleErr(w, err)
}

func (a *API) authorizeMemberChange(w http.ResponseWriter, r *http.Request, orgID, userID string, disabling bool) bool {
	target, err := a.data.GetMembership(r.Context(), orgID, userID)
	if a.handleErr(w, err) {
		return false
	}
	if roleRank(target.Role) > roleRank(a.callerRole(r)) {
		writeError(w, http.StatusForbidden, "cannot modify a member above your own role")
		return false
	}
	if !disabling || target.Role != store.RoleOwner {
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
		if m.Role == store.RoleOwner && m.DisabledAt == nil {
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
