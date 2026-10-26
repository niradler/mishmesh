package controlplane

import (
	"errors"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

func (a *API) loginHandler(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || !a.auth.passwordEnabled {
		writeError(w, http.StatusForbidden, "password login disabled")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := normalizeEmail(req.Email)
	if !a.authRateAllowed(r, email) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	if email == "" {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	user, err := a.data.GetUserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeStoreUnavailable(w)
		return
	}
	hash := ""
	if err == nil {
		hash = user.PasswordHash
	}
	if !passwordMatches(hash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	orgID, ok := a.primaryOrg(r.Context(), user.ID)
	if !ok {
		writeError(w, http.StatusForbidden, "no organization membership")
		return
	}
	if err := a.issueSession(r.Context(), w, user.ID, orgID, authz.AuthMethodPassword, time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	a.writeMe(w, r, http.StatusOK, user, orgID)
}
