package controlplane

import (
	"context"
	"errors"
	"fmt"
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

func (a *API) orgQuota(ctx context.Context, orgID string) (*store.Quota, error) {
	q, err := a.data.GetQuota(ctx, orgID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load quota: %w", err)
	}
	return q, nil
}

func (a *API) enforceAgentQuota(ctx context.Context, orgID string) error {
	q, err := a.orgQuota(ctx, orgID)
	if err != nil {
		return err
	}
	if q == nil || q.MaxAgents <= 0 {
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
	q, err := a.orgQuota(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	if q != nil {
		dto.MaxAgents = q.MaxAgents
		dto.MaxEndpoints = q.MaxEndpoints
		dto.MaxBandwidthBytes = q.MaxBandwidthBytes
	}
	dto.Usage.Agents, err = a.data.CountAgents(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	dto.Usage.Endpoints, err = a.data.CountEndpoints(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
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
	a.getQuotaHandler(w, r)
}

func (a *API) checkEndpointQuota(w http.ResponseWriter, r *http.Request, orgID string) bool {
	q, err := a.orgQuota(r.Context(), orgID)
	if a.handleErr(w, err) {
		return false
	}
	if q == nil || q.MaxEndpoints <= 0 {
		return true
	}
	n, err := a.data.CountEndpoints(r.Context(), orgID)
	if a.handleErr(w, err) {
		return false
	}
	if n >= q.MaxEndpoints {
		writeError(w, http.StatusConflict, "endpoint quota exceeded")
		return false
	}
	return true
}
