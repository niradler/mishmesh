package controlplane

import (
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type auditDTO struct {
	ID        string    `json:"id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *API) listAuditHandler(w http.ResponseWriter, r *http.Request) {
	limit := 200
	events, err := a.data.ListAudit(r.Context(), a.orgScope(r), limit)
	if a.handleErr(w, err) {
		return
	}
	out := make([]auditDTO, 0, len(events))
	for _, e := range events {
		out = append(out, auditDTO{ID: e.ID, Actor: e.Actor, Action: e.Action, Target: e.Target, Detail: e.Detail, CreatedAt: e.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) statusHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	agents, _ := a.data.ListAgents(r.Context(), orgID)
	connected := 0
	for _, ag := range agents {
		if a.connected(ag.ID) {
			connected++
		}
	}
	eps, _ := a.data.ListEndpointsByOrg(r.Context(), orgID)
	byKind := map[string]int{store.KindHTTP: 0, store.KindTCP: 0, store.KindTLS: 0}
	online := 0
	for _, ep := range eps {
		byKind[ep.Kind]++
		if a.conns != nil {
			if _, ok := a.conns.ResolveEndpoint(ep.ID); ok {
				online++
			}
		}
	}
	var usage int64
	if a.conns != nil {
		usage = a.conns.Usage(orgID)
	}
	resp := map[string]any{
		"agents":      map[string]int{"total": len(agents), "connected": connected},
		"endpoints":   map[string]any{"total": len(eps), "online": online, "by_kind": byKind},
		"usage_bytes": usage,
	}
	if q, err := a.data.GetQuota(r.Context(), orgID); err == nil {
		resp["quota"] = map[string]any{
			"max_agents": q.MaxAgents, "max_endpoints": q.MaxEndpoints, "max_bandwidth_bytes": q.MaxBandwidthBytes,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
