package controlplane

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

type endpointSessionDTO struct {
	ID          string    `json:"id"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	Email       string    `json:"email,omitempty"`
	AuthMethod  string    `json:"auth_method"`
	AuthTime    time.Time `json:"auth_time"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type revokedSessionsDTO struct {
	Revoked int `json:"revoked"`
}

func (a *API) listEndpointSessionsHandler(w http.ResponseWriter, r *http.Request) {
	ep, ok := a.scopedEndpoint(w, r)
	if !ok {
		return
	}
	out := []endpointSessionDTO{}
	if a.identity == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	sessions, err := a.identity.EndpointSessions(r.Context(), ep.OrgID, ep.ID)
	if err != nil {
		a.log.Error("list endpoint sessions failed", "endpoint_id", ep.ID, "err", err)
		writeStoreUnavailable(w)
		return
	}
	emails := map[string]string{}
	for _, ts := range sessions {
		out = append(out, endpointSessionDTO{
			ID:          ts.ID,
			SubjectType: ts.SubjectType,
			SubjectID:   ts.SubjectID,
			Email:       a.sessionEmail(r, ts, emails),
			AuthMethod:  ts.AuthMethod,
			AuthTime:    ts.AuthTime,
			CreatedAt:   ts.CreatedAt,
			ExpiresAt:   ts.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) sessionEmail(r *http.Request, ts store.TokenSession, cache map[string]string) string {
	switch ts.SubjectType {
	case store.SubjectGuest:
		return ts.SubjectID
	case store.SubjectUser:
		if email, ok := cache[ts.SubjectID]; ok {
			return email
		}
		user, err := a.data.GetUserByID(r.Context(), ts.SubjectID)
		if err != nil {
			cache[ts.SubjectID] = ""
			return ""
		}
		cache[ts.SubjectID] = user.Email
		return user.Email
	}
	return ""
}

func (a *API) revokeEndpointSessionHandler(w http.ResponseWriter, r *http.Request) {
	ep, ok := a.scopedEndpoint(w, r)
	if !ok {
		return
	}
	if a.identity == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	sessionID := r.PathValue("session_id")
	err := a.identity.RevokeEndpointSession(r.Context(), ep.OrgID, ep.ID, sessionID)
	if errors.Is(err, identity.ErrUnavailable) {
		a.log.Error("revoke endpoint session failed", "endpoint_id", ep.ID, "err", err)
		writeStoreUnavailable(w)
		return
	}
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "endpoint.session.revoke", ep.ID, sessionID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) revokeEndpointSessionsHandler(w http.ResponseWriter, r *http.Request) {
	ep, ok := a.scopedEndpoint(w, r)
	if !ok {
		return
	}
	if a.identity == nil {
		writeJSON(w, http.StatusOK, revokedSessionsDTO{})
		return
	}
	n, err := a.identity.RevokeEndpointSessions(r.Context(), ep.OrgID, ep.ID)
	if err != nil {
		a.log.Error("revoke endpoint sessions failed", "endpoint_id", ep.ID, "err", err)
		writeStoreUnavailable(w)
		return
	}
	a.audit(r, "endpoint.sessions.revoke", ep.ID, strconv.Itoa(n))
	writeJSON(w, http.StatusOK, revokedSessionsDTO{Revoked: n})
}

func (a *API) scopedEndpoint(w http.ResponseWriter, r *http.Request) (*store.Endpoint, bool) {
	return loadScoped(a, w, r, r.PathValue("id"), a.data.GetEndpoint, func(ep *store.Endpoint) string { return ep.OrgID })
}
