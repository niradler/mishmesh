package controlplane

import (
	"context"
	"errors"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/store"
)

var errQuotaAgents = errors.New("agent quota exceeded for org")

func (a *API) SetDefaultQuota(q store.Quota) {
	a.defaultQuota = q
}

func (a *API) applyDefaultQuota(ctx context.Context, orgID string) {
	q := a.defaultQuota
	if q.MaxAgents == 0 && q.MaxEndpoints == 0 && q.MaxBandwidthBytes == 0 {
		return
	}
	if _, err := a.data.GetQuota(ctx, orgID); err == nil {
		return
	}
	q.OrgID = orgID
	_ = a.data.SetQuota(ctx, &q)
}

func (a *API) enforceAgentQuota(ctx context.Context, orgID string) error {
	q, err := a.data.GetQuota(ctx, orgID)
	if err != nil || q.MaxAgents <= 0 {
		return nil
	}
	n, err := a.data.CountAgents(ctx, orgID)
	if err != nil {
		return err
	}
	if n >= q.MaxAgents {
		return errQuotaAgents
	}
	return nil
}

type quotaDTO struct {
	MaxAgents         int   `json:"max_agents"`
	MaxEndpoints      int   `json:"max_endpoints"`
	MaxBandwidthBytes int64 `json:"max_bandwidth_bytes"`
	Usage             struct {
		Agents         int   `json:"agents"`
		Endpoints      int   `json:"endpoints"`
		BandwidthBytes int64 `json:"bandwidth_bytes"`
	} `json:"usage"`
}

func (a *API) getQuotaHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	var dto quotaDTO
	if q, err := a.data.GetQuota(r.Context(), orgID); err == nil {
		dto.MaxAgents = q.MaxAgents
		dto.MaxEndpoints = q.MaxEndpoints
		dto.MaxBandwidthBytes = q.MaxBandwidthBytes
	}
	dto.Usage.Agents, _ = a.data.CountAgents(r.Context(), orgID)
	dto.Usage.Endpoints, _ = a.data.CountEndpoints(r.Context(), orgID)
	if a.conns != nil {
		dto.Usage.BandwidthBytes = a.conns.Usage(orgID)
	}
	writeJSON(w, http.StatusOK, dto)
}

func (a *API) putQuotaHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxAgents         int   `json:"max_agents"`
		MaxEndpoints      int   `json:"max_endpoints"`
		MaxBandwidthBytes int64 `json:"max_bandwidth_bytes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	orgID := a.orgScope(r)
	q := &store.Quota{OrgID: orgID, MaxAgents: req.MaxAgents, MaxEndpoints: req.MaxEndpoints, MaxBandwidthBytes: req.MaxBandwidthBytes}
	if a.handleErr(w, a.data.SetQuota(r.Context(), q)) {
		return
	}
	a.audit(r, "quota.update", orgID, "")
	writeJSON(w, http.StatusOK, q)
}
