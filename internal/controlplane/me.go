package controlplane

import (
	"context"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/store"
)

type membershipDTO struct {
	OrgID   string `json:"org_id"`
	OrgName string `json:"org_name"`
	Role    string `json:"role"`
}

type meDTO struct {
	ID          string          `json:"id"`
	Email       string          `json:"email"`
	Name        string          `json:"name"`
	ActiveOrgID string          `json:"active_org_id"`
	Role        string          `json:"role"`
	Memberships []membershipDTO `json:"memberships"`
}

func (a *API) writeMe(w http.ResponseWriter, r *http.Request, status int, user *store.User, activeOrgID string) {
	memberships, err := a.data.ListMembershipsByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	dto := meDTO{ID: user.ID, Email: user.Email, Name: user.Name, ActiveOrgID: activeOrgID, Memberships: make([]membershipDTO, 0, len(memberships))}
	for _, m := range memberships {
		name := m.OrgID
		if org, err := a.data.GetOrg(r.Context(), m.OrgID); err == nil {
			name = org.Name
		}
		if m.OrgID == activeOrgID {
			dto.Role = m.Role
		}
		dto.Memberships = append(dto.Memberships, membershipDTO{OrgID: m.OrgID, OrgName: name, Role: m.Role})
	}
	writeJSON(w, status, dto)
}

func (a *API) meHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.resolveSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	a.writeMe(w, r, http.StatusOK, su.user, su.orgID)
}

func (a *API) switchOrgHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.resolveSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		OrgID string `json:"org_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if m, err := a.data.GetMembership(r.Context(), req.OrgID, su.user.ID); err != nil || m.DisabledAt != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.data.DeleteSession(r.Context(), store.HashToken(c.Value))
	}
	if err := a.issueSession(r.Context(), w, su.user.ID, req.OrgID, su.authMethod, su.authTime); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	a.writeMe(w, r, http.StatusOK, su.user, req.OrgID)
}

func (a *API) primaryOrg(ctx context.Context, userID string) (string, bool) {
	memberships, err := a.data.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return "", false
	}
	for _, m := range memberships {
		if m.DisabledAt == nil {
			return m.OrgID, true
		}
	}
	return "", false
}
