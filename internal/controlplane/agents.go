package controlplane

import (
	"errors"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

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

func (a *API) createAgentHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		OrgID string `json:"org_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	orgID := a.orgScope(r)
	if req.OrgID != "" && req.OrgID != orgID {
		if !a.isAdmin(r) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		orgID = req.OrgID
	}
	agent, raw, err := a.createAgent(r.Context(), orgID, req.Name)
	if errors.Is(err, errQuotaAgents) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "agent.create", agent.ID, agent.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent": a.toAgentDTO(agent),
		"token": raw,
	})
}

func (a *API) listAgentsHandler(w http.ResponseWriter, r *http.Request) {
	agents, err := a.data.ListAgents(r.Context(), a.orgScope(r))
	writeList(a, w, agents, err, a.toAgentDTO)
}

func (a *API) getAgentHandler(w http.ResponseWriter, r *http.Request) {
	ag, ok := a.scopedAgent(w, r, r.PathValue("id"))
	if !ok {
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
	if _, ok := a.scopedAgent(w, r, r.PathValue("id")); !ok {
		return
	}
	ag, err := a.updateAgent(r.Context(), r.PathValue("id"), req.Name, req.Status)
	if a.handleErr(w, err) {
		return
	}
	if req.Status != "" {
		a.flushDecisions(ag.OrgID)
	}
	a.audit(r, patchAgentAuditAction(req.Status), ag.ID, req.Name)
	writeJSON(w, http.StatusOK, a.toAgentDTO(ag))
}

func patchAgentAuditAction(status string) string {
	switch status {
	case store.AgentRevoked:
		return "agent.revoke"
	case store.AgentDisabled:
		return "agent.disable"
	case store.AgentActive:
		return "agent.enable"
	}
	return "agent.update"
}

func (a *API) deleteAgentHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.scopedAgent(w, r, r.PathValue("id")); !ok {
		return
	}
	err := a.deleteAgent(r.Context(), r.PathValue("id"))
	if errors.Is(err, errAgentNotRevoked) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "agent.delete", r.PathValue("id"), "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) rotateTokenHandler(w http.ResponseWriter, r *http.Request) {
	ag, ok := a.scopedAgent(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	raw, err := a.rotateToken(r.Context(), ag)
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "agent.rotate", ag.ID, "")
	writeJSON(w, http.StatusCreated, map[string]string{"token": raw})
}

func (a *API) revokeAgentHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.scopedAgent(w, r, r.PathValue("id")); !ok {
		return
	}
	if a.handleErr(w, a.revokeAgent(r.Context(), r.PathValue("id"))) {
		return
	}
	a.audit(r, "agent.revoke", r.PathValue("id"), "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (a *API) listEndpointsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.scopedAgent(w, r, r.PathValue("id")); !ok {
		return
	}
	eps, err := a.data.ListEndpointsByAgent(r.Context(), r.PathValue("id"))
	writeList(a, w, eps, err, a.toEndpointDTO)
}

func (a *API) listTokensHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.scopedAgent(w, r, r.PathValue("id")); !ok {
		return
	}
	toks, err := a.data.ListTokensByAgent(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, mapSlice(toks, toTokenDTO))
}

func toTokenDTO(t *store.Token) tokenDTO {
	return tokenDTO{ID: t.ID, CreatedAt: t.CreatedAt, RevokedAt: t.RevokedAt}
}

func (a *API) scopedAgent(w http.ResponseWriter, r *http.Request, id string) (*store.Agent, bool) {
	return loadScoped(a, w, r, id, a.data.GetAgent, func(ag *store.Agent) string {
		if a.isAdmin(r) {
			return a.orgScope(r)
		}
		return ag.OrgID
	})
}
