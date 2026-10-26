package controlplane

import (
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func (a *API) createOrgHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	su, hasSession := a.resolveSession(r)
	if hasSession && !a.isAdmin(r) && a.maxOrgsPerUser > 0 {
		owned, err := a.ownedOrgCount(r.Context(), su.user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create org failed")
			return
		}
		if owned >= a.maxOrgsPerUser {
			writeError(w, http.StatusConflict, "organization limit reached")
			return
		}
	}
	org, err := a.createOrg(r.Context(), req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create org failed")
		return
	}
	if hasSession {
		if err := a.data.CreateMembership(r.Context(), &store.Membership{OrgID: org.ID, UserID: su.user.ID, Role: store.RoleOwner, CreatedAt: a.now()}); err != nil {
			a.log.Warn("create owner membership", "org", org.ID, "err", err)
			writeError(w, http.StatusInternalServerError, "create org failed")
			return
		}
	}
	a.audit(r, "org.create", org.ID, org.Name)
	writeJSON(w, http.StatusCreated, toOrgDTO(org))
}

func (a *API) getOrgHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !a.isAdmin(r) {
		su, ok := a.resolveSession(r)
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if _, err := a.data.GetMembership(r.Context(), id, su.user.ID); err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
	}
	org, err := a.data.GetOrg(r.Context(), id)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toOrgDTO(org))
}

type orgDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func toOrgDTO(o *store.Org) orgDTO {
	return orgDTO{ID: o.ID, Name: o.Name, CreatedAt: o.CreatedAt}
}
