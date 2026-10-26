package controlplane

import (
	"context"
	"errors"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

type callerKey struct{}

type caller struct {
	orgID string
	actor string
	role  string
	admin bool
	kind  authz.EntityType
}

func callerOf(r *http.Request) caller {
	c, _ := r.Context().Value(callerKey{}).(caller)
	return c
}

func (a *API) orgScope(r *http.Request) string {
	if v := callerOf(r).orgID; v != "" {
		return v
	}
	if v := r.URL.Query().Get("org_id"); v != "" {
		return v
	}
	return defaultOrgID
}

func (a *API) isAdmin(r *http.Request) bool {
	return callerOf(r).admin
}

func (a *API) actor(r *http.Request) string {
	if v := callerOf(r).actor; v != "" {
		return v
	}
	return "system"
}

func (a *API) audit(r *http.Request, action, target, detail string) {
	a.auditAs(r, a.orgScope(r), a.actor(r), action, target, detail)
}

func (a *API) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, ok := a.authorize(w, r)
		if !ok {
			return
		}
		h(w, r.WithContext(ctx))
	}
}

var operatorOnlyActions = map[authz.Action]struct{}{
	authz.ActionQuotaWrite: {},
}

func (a *API) require(action authz.Action, h http.HandlerFunc) http.HandlerFunc {
	return a.guard(func(w http.ResponseWriter, r *http.Request) {
		if !a.permitted(r, action) {
			writeError(w, http.StatusForbidden, "permission denied: "+string(action))
			return
		}
		h(w, r)
	})
}

func (a *API) permitted(r *http.Request, action authz.Action) bool {
	if a.isAdmin(r) {
		return true
	}
	if _, restricted := operatorOnlyActions[action]; restricted {
		return false
	}
	c := callerOf(r)
	org := a.orgScope(r)
	return a.authorizerFor(r.Context(), org).Authorize(authz.Principal{ID: a.actor(r), Type: c.kind, Role: c.role, Org: org}, action)
}

func (a *API) authorize(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	tok := token.Bearer(r.Header.Get("Authorization"))
	if a.adminToken != "" && tok != "" {
		if !store.ConstantTimeEqualHash(tok, a.adminToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return nil, false
		}
		return a.authContext(r, caller{orgID: a.queryOrg(r), actor: "admin", role: store.RoleOwner, admin: true}), true
	}
	if a.authEnabled() {
		if err := a.csrfCheck(r); err != nil {
			writeCSRFError(w, err)
			return nil, false
		}
		su, err := a.lookupSession(r)
		if err != nil {
			if errors.Is(err, errNoSession) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
			} else {
				writeStoreUnavailable(w)
			}
			return nil, false
		}
		return a.authContext(r, caller{orgID: su.orgID, actor: su.user.Email, role: su.role}), true
	}
	if a.adminToken != "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	return a.authContext(r, caller{orgID: a.queryOrg(r), actor: "system", role: store.RoleOwner, admin: true}), true
}

func (a *API) authContext(r *http.Request, c caller) context.Context {
	return context.WithValue(r.Context(), callerKey{}, c)
}

func (a *API) queryOrg(r *http.Request) string {
	if v := r.URL.Query().Get("org_id"); v != "" {
		return v
	}
	return defaultOrgID
}
