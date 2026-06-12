package controlplane

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type API struct {
	data       store.DataStore
	conns      store.ConnectionStore
	log        *slog.Logger
	adminToken string
}

func New(data store.DataStore, conns store.ConnectionStore, adminToken string, log *slog.Logger) *API {
	if log == nil {
		log = slog.Default()
	}
	return &API{data: data, conns: conns, log: log, adminToken: adminToken}
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.health)

	mux.HandleFunc("POST /api/v1/orgs", a.guard(a.createOrgHandler))
	mux.HandleFunc("GET /api/v1/orgs/{id}", a.guard(a.getOrgHandler))

	mux.HandleFunc("POST /api/v1/agents", a.guard(a.createAgentHandler))
	mux.HandleFunc("GET /api/v1/agents", a.guard(a.listAgentsHandler))
	mux.HandleFunc("GET /api/v1/agents/{id}", a.guard(a.getAgentHandler))
	mux.HandleFunc("PATCH /api/v1/agents/{id}", a.guard(a.patchAgentHandler))
	mux.HandleFunc("DELETE /api/v1/agents/{id}", a.guard(a.deleteAgentHandler))
	mux.HandleFunc("POST /api/v1/agents/{id}/rotate", a.guard(a.rotateTokenHandler))
	mux.HandleFunc("POST /api/v1/agents/{id}/revoke", a.guard(a.revokeAgentHandler))
	mux.HandleFunc("GET /api/v1/agents/{id}/endpoints", a.guard(a.listEndpointsHandler))
	mux.HandleFunc("GET /api/v1/agents/{id}/tokens", a.guard(a.listTokensHandler))
}

func (a *API) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.adminToken != "" {
			if !store.ConstantTimeEqualHash(bearerToken(r), a.adminToken) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		h(w, r)
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

type agentDTO struct {
	ID         string     `json:"id"`
	OrgID      string     `json:"org_id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Connected  bool       `json:"connected"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type tokenDTO struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func (a *API) toAgentDTO(ag *store.Agent) agentDTO {
	return agentDTO{
		ID:         ag.ID,
		OrgID:      ag.OrgID,
		Name:       ag.Name,
		Status:     ag.Status,
		Connected:  a.connected(ag.ID),
		CreatedAt:  ag.CreatedAt,
		LastSeenAt: ag.LastSeenAt,
	}
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

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
	org, err := a.createOrg(r.Context(), req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create org failed")
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (a *API) getOrgHandler(w http.ResponseWriter, r *http.Request) {
	org, err := a.data.GetOrg(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (a *API) createAgentHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		OrgID string `json:"org_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	agent, raw, err := a.createAgent(r.Context(), req.OrgID, req.Name)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent": a.toAgentDTO(agent),
		"token": raw,
	})
}

func (a *API) listAgentsHandler(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		orgID = defaultOrgID
	}
	agents, err := a.data.ListAgents(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	out := make([]agentDTO, 0, len(agents))
	for _, ag := range agents {
		out = append(out, a.toAgentDTO(ag))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getAgentHandler(w http.ResponseWriter, r *http.Request) {
	ag, err := a.data.GetAgent(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, a.toAgentDTO(ag))
}

func (a *API) patchAgentHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ag, err := a.updateAgent(r.Context(), r.PathValue("id"), req.Name, req.Status)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, a.toAgentDTO(ag))
}

func (a *API) deleteAgentHandler(w http.ResponseWriter, r *http.Request) {
	err := a.deleteAgent(r.Context(), r.PathValue("id"))
	if errors.Is(err, errAgentNotRevoked) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if a.handleErr(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) rotateTokenHandler(w http.ResponseWriter, r *http.Request) {
	ag, err := a.data.GetAgent(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	raw, err := a.issueToken(r.Context(), ag)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": raw})
}

func (a *API) revokeAgentHandler(w http.ResponseWriter, r *http.Request) {
	if a.handleErr(w, a.revokeAgent(r.Context(), r.PathValue("id"))) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (a *API) listEndpointsHandler(w http.ResponseWriter, r *http.Request) {
	eps, err := a.data.ListEndpointsByAgent(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, eps)
}

func (a *API) listTokensHandler(w http.ResponseWriter, r *http.Request) {
	toks, err := a.data.ListTokensByAgent(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	out := make([]tokenDTO, 0, len(toks))
	for _, t := range toks {
		out = append(out, tokenDTO{ID: t.ID, CreatedAt: t.CreatedAt, RevokedAt: t.RevokedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return true
	}
	a.log.Warn("control api error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
