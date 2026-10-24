package controlplane

import (
	"net/http"
	"strconv"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type accessEventDTO struct {
	ID            string    `json:"id"`
	At            time.Time `json:"at"`
	Source        string    `json:"source"`
	Decision      string    `json:"decision"`
	Reason        string    `json:"reason,omitempty"`
	PrincipalType string    `json:"principal_type,omitempty"`
	PrincipalID   string    `json:"principal_id,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	ExitAgentID   string    `json:"exit_agent_id,omitempty"`
	EndpointID    string    `json:"endpoint_id,omitempty"`
	Destination   string    `json:"destination,omitempty"`
	PolicyIDs     []string  `json:"policy_ids,omitempty"`
	ClientIP      string    `json:"client_ip,omitempty"`
	Method        string    `json:"method,omitempty"`
	Path          string    `json:"path,omitempty"`
	Status        int       `json:"status,omitempty"`
	Protocol      string    `json:"protocol,omitempty"`
	BytesIn       int64     `json:"bytes_in"`
	BytesOut      int64     `json:"bytes_out"`
	DurationMs    int64     `json:"duration_ms"`
	LatencyMs     int64     `json:"latency_ms"`
}

func parseAccessLogQuery(r *http.Request) (store.AccessLogQuery, string) {
	v := r.URL.Query()
	q := store.AccessLogQuery{
		Decision:    v.Get("decision"),
		Source:      v.Get("source"),
		PrincipalID: v.Get("principal"),
		EndpointID:  v.Get("endpoint"),
	}
	switch q.Decision {
	case "", store.AccessAllow, store.AccessDeny:
	default:
		return q, "decision must be allow or deny"
	}
	switch q.Source {
	case "", store.AccessSourceIngress, store.AccessSourceConnect, store.AccessSourceEgress:
	default:
		return q, "source must be ingress, connect or egress"
	}
	for name, dst := range map[string]*time.Time{"since": &q.Since, "until": &q.Until} {
		raw := v.Get(name)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, name + " must be an RFC 3339 time"
		}
		*dst = t
	}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > store.MaxAccessLogLimit {
			return q, "limit must be between 1 and " + strconv.Itoa(store.MaxAccessLogLimit)
		}
		q.Limit = n
	}
	return q, ""
}

func (a *API) listAccessLogHandler(w http.ResponseWriter, r *http.Request) {
	q, problem := parseAccessLogQuery(r)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	events, err := a.data.ListAccessEvents(r.Context(), a.orgScope(r), q)
	if a.handleErr(w, err) {
		return
	}
	out := make([]accessEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, accessEventDTO{
			ID: e.ID, At: e.At, Source: e.Source, Decision: e.Decision, Reason: e.Reason,
			PrincipalType: e.PrincipalType, PrincipalID: e.PrincipalID, SessionID: e.SessionID,
			AgentID: e.AgentID, ExitAgentID: e.ExitAgentID, EndpointID: e.EndpointID, Destination: e.Destination, PolicyIDs: e.PolicyIDs,
			ClientIP: e.ClientIP, Method: e.Method, Path: e.Path, Status: e.Status, Protocol: e.Protocol,
			BytesIn: e.BytesIn, BytesOut: e.BytesOut, DurationMs: e.DurationMs, LatencyMs: e.LatencyMs,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
