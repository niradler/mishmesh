package controlplane

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/store"
)

type API struct {
	data store.DataStore
	log  *slog.Logger
}

func New(data store.DataStore, log *slog.Logger) *API {
	if log == nil {
		log = slog.Default()
	}
	return &API{data: data, log: log}
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.health)
	mux.HandleFunc("GET /api/v1/agents", a.listAgents)
	mux.HandleFunc("GET /api/v1/agents/{id}/endpoints", a.listEndpoints)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) listAgents(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		writeError(w, http.StatusBadRequest, "org_id query parameter required")
		return
	}
	agents, err := a.data.ListAgents(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list agents failed")
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (a *API) listEndpoints(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	eps, err := a.data.ListEndpointsByAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list endpoints failed")
		return
	}
	writeJSON(w, http.StatusOK, eps)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
