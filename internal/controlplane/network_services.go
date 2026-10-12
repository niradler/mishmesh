package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

type serviceInput struct {
	Name                string          `json:"name"`
	Driver              string          `json:"driver"`
	Spec                json.RawMessage `json:"spec"`
	IdleTimeoutSeconds  int             `json:"idle_timeout_seconds"`
	StartTimeoutSeconds int             `json:"start_timeout_seconds"`
}

type serviceStatusDTO struct {
	State        string    `json:"state"`
	Active       int       `json:"active"`
	LastActivity time.Time `json:"last_activity,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
	StartedBy    string    `json:"started_by,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type serviceDTO struct {
	ID                  string           `json:"id"`
	Name                string           `json:"name"`
	Driver              string           `json:"driver"`
	Spec                services.Spec    `json:"spec"`
	IdleTimeoutSeconds  int              `json:"idle_timeout_seconds"`
	StartTimeoutSeconds int              `json:"start_timeout_seconds"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	Status              serviceStatusDTO `json:"status"`
	Endpoints           []string         `json:"endpoints"`
}

const redactedEnvValue = "<redacted>"

func redactExecEnv(spec services.Spec) services.Spec {
	if spec.Exec == nil || len(spec.Exec.Env) == 0 {
		return spec
	}
	execSpec := *spec.Exec
	execSpec.Env = make(map[string]string, len(spec.Exec.Env))
	for key := range spec.Exec.Env {
		execSpec.Env[key] = redactedEnvValue
	}
	spec.Exec = &execSpec
	return spec
}

func (a *API) toServiceDTO(r *http.Request, rec *store.Service, linked []*store.Endpoint) serviceDTO {
	spec, err := services.DecodeSpec(rec.Spec)
	if !a.permitted(r, authz.ActionHostExec) {
		spec = redactExecEnv(spec)
	}
	status := serviceStatusDTO{State: services.StateUnknown, UpdatedAt: rec.UpdatedAt}
	if err != nil {
		status.State, status.LastError = services.StateError, err.Error()
	}
	if a.services != nil {
		if st, ok := a.services.Status(rec.ID); ok {
			status = serviceStatusDTO{State: st.State, Active: st.Active, LastActivity: st.LastActivity,
				LastError: st.LastError, StartedBy: st.StartedBy, UpdatedAt: st.UpdatedAt}
		}
	}
	ids := make([]string, 0, len(linked))
	for _, ep := range linked {
		ids = append(ids, ep.ID)
	}
	return serviceDTO{
		ID: rec.ID, Name: rec.Name, Driver: rec.Driver, Spec: spec,
		IdleTimeoutSeconds: rec.IdleTimeoutSeconds, StartTimeoutSeconds: rec.StartTimeoutSeconds,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, Status: status, Endpoints: ids,
	}
}

func (a *API) linkedEndpoints(ctx context.Context, orgID string) (map[string][]*store.Endpoint, error) {
	eps, err := a.data.ListEndpointsByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]*store.Endpoint)
	for _, ep := range eps {
		if id := endpointServiceID(ep); id != "" {
			out[id] = append(out[id], ep)
		}
	}
	return out, nil
}

func (a *API) reloadServices(ctx context.Context) error {
	if a.services == nil {
		return nil
	}
	a.servicesMu.Lock()
	defer a.servicesMu.Unlock()
	recs, err := a.data.ListAllServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	defs := make([]services.Definition, 0, len(recs))
	for _, rec := range recs {
		def, err := services.FromRecord(rec)
		if err != nil {
			a.log.Error("service skipped", "id", rec.ID, "org", rec.OrgID, "err", err)
			continue
		}
		defs = append(defs, def)
	}
	a.services.Apply(defs)
	return nil
}

func (a *API) LoadServices(ctx context.Context) error {
	return a.reloadServices(ctx)
}

func (a *API) scopedService(w http.ResponseWriter, r *http.Request) (*store.Service, bool) {
	rec, err := a.data.GetService(r.Context(), r.PathValue("id"))
	if err == nil && rec.OrgID != a.orgScope(r) {
		err = store.ErrNotFound
	}
	if a.handleErr(w, err) {
		return nil, false
	}
	return rec, true
}

func (a *API) buildService(w http.ResponseWriter, r *http.Request, existing *store.Service) (*store.Service, bool) {
	var in serviceInput
	if !decodeJSON(w, r, &in) {
		return nil, false
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Driver = strings.TrimSpace(in.Driver)
	spec, err := services.DecodeSpec(in.Spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	hostCommands := services.UsesHostCommands(in.Driver, spec)
	spec = services.Normalize(in.Driver, spec)
	if err := services.Validate(in.Name, in.Driver, spec, in.IdleTimeoutSeconds, in.StartTimeoutSeconds); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if existing != nil && storedUsesHostCommands(existing) {
		hostCommands = true
	}
	if hostCommands {
		if err := a.hostAccess(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return nil, false
		}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, "spec: "+err.Error())
		return nil, false
	}
	now := a.now()
	rec := &store.Service{
		ID: store.NewID("svc"), OrgID: a.orgScope(r), CreatedAt: now,
		Name: in.Name, Driver: in.Driver, Spec: raw,
		IdleTimeoutSeconds: in.IdleTimeoutSeconds, StartTimeoutSeconds: in.StartTimeoutSeconds, UpdatedAt: now,
	}
	if existing != nil {
		rec.ID, rec.OrgID, rec.CreatedAt = existing.ID, existing.OrgID, existing.CreatedAt
	}
	return rec, true
}

func (a *API) writeServiceErr(w http.ResponseWriter, err error, name string) bool {
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, fmt.Sprintf("a service named %q already exists in this org", name))
		return true
	}
	return a.handleErr(w, err)
}

func (a *API) afterServiceChange(r *http.Request) {
	if err := a.reloadServices(r.Context()); err != nil {
		a.log.Error("reload services after change failed", "err", err)
	}
}

func (a *API) respondService(w http.ResponseWriter, r *http.Request, status int, rec *store.Service) {
	linked, err := a.linkedEndpoints(r.Context(), rec.OrgID)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, status, a.toServiceDTO(r, rec, linked[rec.ID]))
}

func (a *API) listServicesHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	recs, err := a.data.ListServices(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	linked, err := a.linkedEndpoints(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	out := make([]serviceDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, a.toServiceDTO(r, rec, linked[rec.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getServiceHandler(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.scopedService(w, r)
	if !ok {
		return
	}
	a.respondService(w, r, http.StatusOK, rec)
}

func (a *API) createServiceHandler(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.buildService(w, r, nil)
	if !ok {
		return
	}
	if a.writeServiceErr(w, a.data.CreateService(r.Context(), rec), rec.Name) {
		return
	}
	a.audit(r, "service.create", rec.ID, rec.Name)
	a.afterServiceChange(r)
	a.respondService(w, r, http.StatusCreated, rec)
}

func (a *API) updateServiceHandler(w http.ResponseWriter, r *http.Request) {
	existing, ok := a.scopedService(w, r)
	if !ok {
		return
	}
	rec, ok := a.buildService(w, r, existing)
	if !ok {
		return
	}
	if a.writeServiceErr(w, a.data.UpdateService(r.Context(), rec), rec.Name) {
		return
	}
	a.audit(r, "service.update", rec.ID, rec.Name)
	a.afterServiceChange(r)
	a.respondService(w, r, http.StatusOK, rec)
}

func storedUsesHostCommands(rec *store.Service) bool {
	spec, err := services.DecodeSpec(rec.Spec)
	return err != nil || services.UsesHostCommands(rec.Driver, spec)
}

func (a *API) deleteServiceHandler(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.scopedService(w, r)
	if !ok {
		return
	}
	if storedUsesHostCommands(rec) {
		if err := a.hostAccess(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	linked, err := a.linkedEndpoints(r.Context(), rec.OrgID)
	if a.handleErr(w, err) {
		return
	}
	if eps := linked[rec.ID]; len(eps) > 0 {
		ids := make([]string, 0, len(eps))
		for _, ep := range eps {
			ids = append(ids, ep.ID)
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     fmt.Sprintf("service %q is linked to %d endpoint(s); unlink them first", rec.Name, len(ids)),
			"endpoints": ids,
		})
		return
	}
	if a.handleErr(w, a.data.DeleteService(r.Context(), rec.ID)) {
		return
	}
	a.audit(r, "service.delete", rec.ID, rec.Name)
	a.afterServiceChange(r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) controllableService(w http.ResponseWriter, r *http.Request) (*store.Service, bool) {
	rec, ok := a.scopedService(w, r)
	if !ok {
		return nil, false
	}
	if a.services == nil {
		writeError(w, http.StatusServiceUnavailable, "services are not running on this server")
		return nil, false
	}
	if _, loaded := a.services.Status(rec.ID); !loaded {
		writeError(w, http.StatusConflict, "service is not loaded; check its definition")
		return nil, false
	}
	return rec, true
}

func (a *API) startServiceHandler(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.controllableService(w, r)
	if !ok {
		return
	}
	linked, err := a.linkedEndpoints(r.Context(), rec.OrgID)
	if a.handleErr(w, err) {
		return
	}
	ready := ""
	for _, ep := range linked[rec.ID] {
		if ep.Policy != nil && len(ep.Policy.ProxyTargets) > 0 {
			ready = ep.Policy.ProxyTargets[0]
			break
		}
	}
	if err := a.services.EnsureRunning(r.Context(), rec.ID, ready); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "service.start", rec.ID, rec.Name)
	a.respondService(w, r, http.StatusOK, rec)
}

func (a *API) stopServiceHandler(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.controllableService(w, r)
	if !ok {
		return
	}
	if err := a.services.Stop(r.Context(), rec.ID); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "service.stop", rec.ID, rec.Name)
	a.respondService(w, r, http.StatusOK, rec)
}
