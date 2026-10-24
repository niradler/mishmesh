package controlplane

import (
	"errors"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type egressCredentialDTO struct {
	Name         string    `json:"name"`
	Header       string    `json:"header"`
	Prefix       string    `json:"prefix,omitempty"`
	Secret       string    `json:"secret"`
	AgentIDs     []string  `json:"agent_ids"`
	Destinations []string  `json:"destinations"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toEgressCredentialDTO(c *store.EgressCredential) egressCredentialDTO {
	agents := c.AgentIDs
	if agents == nil {
		agents = []string{}
	}
	destinations := c.Destinations
	if destinations == nil {
		destinations = []string{}
	}
	return egressCredentialDTO{Name: c.Name, Header: c.Header, Prefix: c.Prefix, Secret: c.Secret, AgentIDs: agents, Destinations: destinations, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func (a *API) listEgressCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	list, err := a.data.ListEgressCredentials(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	out := make([]egressCredentialDTO, 0, len(list))
	for _, c := range list {
		out = append(out, toEgressCredentialDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getEgressCredentialHandler(w http.ResponseWriter, r *http.Request) {
	c, err := a.data.GetEgressCredential(r.Context(), a.orgScope(r), r.PathValue("name"))
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toEgressCredentialDTO(c))
}

func (a *API) putEgressCredentialHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Header       string   `json:"header"`
		Prefix       string   `json:"prefix"`
		Secret       string   `json:"secret"`
		AgentIDs     []string `json:"agent_ids"`
		Destinations []string `json:"destinations"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	orgID := a.orgScope(r)
	c := &store.EgressCredential{
		OrgID:        orgID,
		Name:         r.PathValue("name"),
		Header:       req.Header,
		Prefix:       req.Prefix,
		Secret:       req.Secret,
		AgentIDs:     dedupe(req.AgentIDs),
		Destinations: req.Destinations,
		UpdatedAt:    a.now().UTC(),
	}
	if err := c.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, id := range c.AgentIDs {
		ag, err := a.data.GetAgent(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && ag.OrgID != orgID) {
			writeError(w, http.StatusBadRequest, "agent_ids contains an unknown agent")
			return
		}
		if a.handleErr(w, err) {
			return
		}
	}
	if a.handleErr(w, a.data.PutEgressCredential(r.Context(), c)) {
		return
	}
	a.flushDecisions(orgID)
	a.audit(r, "egress_credential.put", c.Name, "")
	writeJSON(w, http.StatusOK, toEgressCredentialDTO(c))
}

func (a *API) deleteEgressCredentialHandler(w http.ResponseWriter, r *http.Request) {
	orgID, name := a.orgScope(r), r.PathValue("name")
	if a.handleErr(w, a.data.DeleteEgressCredential(r.Context(), orgID, name)) {
		return
	}
	a.flushDecisions(orgID)
	a.audit(r, "egress_credential.delete", name, "")
	w.WriteHeader(http.StatusNoContent)
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
