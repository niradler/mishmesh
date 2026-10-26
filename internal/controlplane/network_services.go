package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type serviceInput struct {
	Name                string          `json:"name"`
	Driver              string          `json:"driver"`
	Config              json.RawMessage `json:"config"`
	Ready               json.RawMessage `json:"ready"`
	Requires            []string        `json:"requires"`
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
	Config              json.RawMessage  `json:"config"`
	Ready               json.RawMessage  `json:"ready"`
	Requires            []string         `json:"requires"`
	RequiredBy          []string         `json:"required_by"`
	IdleTimeoutSeconds  int              `json:"idle_timeout_seconds"`
	StartTimeoutSeconds int              `json:"start_timeout_seconds"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
	Status              serviceStatusDTO `json:"status"`
	Endpoints           []string         `json:"endpoints"`
}

const redactedEnvValue = "<redacted>"

func (a *API) toServiceDTO(r *http.Request, rec *store.Service, linked []*store.Endpoint) serviceDTO {
	def, err := services.FromRecordWithRegistry(rec, a.driverRegistry())
	config := def.Config
	if driver, found := a.driverRegistry().Lookup(def.Driver); found {
		config, err = typedfields.Redact(driver.Describe().Fields, config)
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
	if status.LastError != "" {
		status.LastError = "service operation failed; check the service configuration and server diagnostics"
	}
	ids := make([]string, 0, len(linked))
	for _, ep := range linked {
		ids = append(ids, ep.ID)
	}
	requiredBy := []string{}
	if dependents, listErr := a.data.ListServiceDependents(r.Context(), rec.OrgID, rec.ID); listErr == nil {
		for _, dependent := range dependents {
			requiredBy = append(requiredBy, dependent.ID)
		}
	}
	ready, _ := json.Marshal(def.Ready)
	return serviceDTO{
		ID: rec.ID, Name: rec.Name, Driver: def.Driver, Config: config, Ready: ready,
		Requires: append([]string{}, rec.Requires...), RequiredBy: requiredBy,
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
	if err := a.lockServiceMutation(ctx); err != nil {
		return err
	}
	defer a.unlockServiceMutation()
	return a.reloadServicesLocked(ctx)
}

func (a *API) reloadServicesLocked(ctx context.Context) error {
	if a.services == nil {
		return nil
	}
	recs, err := a.data.ListAllServices(ctx)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	defs, err := a.buildDefinitions(ctx, recs, a.data.ListEndpointsByOrg)
	if err != nil {
		return err
	}
	a.services.Apply(defs)
	return nil
}

func (a *API) LoadServices(ctx context.Context) error {
	return a.reloadServices(ctx)
}

func (a *API) scopedService(w http.ResponseWriter, r *http.Request) (*store.Service, bool) {
	return loadScoped(a, w, r, r.PathValue("id"), a.data.GetService, func(rec *store.Service) string { return rec.OrgID })
}

func (a *API) buildService(w http.ResponseWriter, r *http.Request, existing *store.Service) (*store.Service, bool) {
	var in serviceInput
	if !decodeJSON(w, r, &in) {
		return nil, false
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Driver = strings.TrimSpace(in.Driver)
	now := a.now()
	rec := &store.Service{
		ID: store.NewID("svc"), OrgID: a.orgScope(r), CreatedAt: now,
		Name: in.Name, Driver: in.Driver, Config: in.Config, Ready: in.Ready, Requires: in.Requires,
		IdleTimeoutSeconds: in.IdleTimeoutSeconds, StartTimeoutSeconds: in.StartTimeoutSeconds, UpdatedAt: now,
	}
	if existing != nil {
		rec.ID, rec.OrgID, rec.CreatedAt = existing.ID, existing.OrgID, existing.CreatedAt
	}
	driver, found := a.driverRegistry().Lookup(rec.Driver)
	if !found {
		writeError(w, http.StatusBadRequest, "unknown service driver")
		return nil, false
	}
	if err := preserveServiceSecrets(driver.Describe().Fields, rec, existing); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if _, err := services.FromRecordWithRegistry(rec, a.driverRegistry()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid service definition: "+err.Error())
		return nil, false
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(rec.Config, &values); err != nil {
		writeError(w, http.StatusBadRequest, "invalid service config")
		return nil, false
	}
	for _, field := range driver.Describe().Fields {
		if field.Kind != "connection" {
			continue
		}
		var id string
		if err := json.Unmarshal(values[field.Key], &id); err != nil || id == "" {
			continue
		}
		connection, err := a.data.GetConnection(r.Context(), id)
		if err != nil || connection.OrgID != rec.OrgID || connection.Type != field.ConnType {
			writeError(w, http.StatusBadRequest, "connection must reference a matching connection in this organization")
			return nil, false
		}
		rec.ConnectionIDs = append(rec.ConnectionIDs, id)
	}
	return rec, true
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
	r, release, ok := a.beginServiceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	rec, ok := a.buildService(w, r, nil)
	if !ok {
		return
	}
	expected, definitions, ok := a.authorizeServiceGraph(w, r, rec, nil)
	if !ok {
		return
	}
	err := a.reserveService(r, definitions, func(reservation services.LifecycleReservation) error {
		if err := a.snapshotMutation(r, rec.OrgID, expected, func(mutation store.ServiceMutation) error {
			return mutation.CreateService(r.Context(), rec)
		}); err != nil {
			return err
		}
		if reservation == nil {
			return nil
		}
		return a.applyServices(r, rec.OrgID, append(expected, rec), reservation, nil)
	})
	if a.handleStoreErr(w, err, "service name already exists or references changed; reload and retry") {
		return
	}
	a.audit(r, "service.create", rec.ID, rec.Name)
	a.respondService(w, r, http.StatusCreated, rec)
}

func (a *API) updateServiceHandler(w http.ResponseWriter, r *http.Request) {
	r, release, ok := a.beginServiceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	existing, ok := a.scopedService(w, r)
	if !ok {
		return
	}
	rec, ok := a.buildService(w, r, existing)
	if !ok {
		return
	}
	expected, definitions, ok := a.authorizeServiceGraph(w, r, rec, existing)
	if !ok {
		return
	}
	proposed, _ := services.FromRecordWithRegistry(rec, a.driverRegistry())
	restart := r.URL.Query().Get("restart") == "true"
	if value := r.URL.Query().Get("restart"); value != "" && value != "true" && value != "false" {
		writeError(w, http.StatusBadRequest, "restart must be true or false")
		return
	}
	phase, saved := "validation", false
	var operationErr error
	err := a.reserveService(r, definitions, func(reservation services.LifecycleReservation) error {
		if reservation != nil {
			if err := reservation.CheckUpdate(rec.ID, proposed, restart); err != nil {
				return err
			}
		}
		var authoritative []*store.Service
		err := a.snapshotMutation(r, rec.OrgID, expected, func(mutation store.ServiceMutation) error {
			if restart && reservation != nil {
				phase = "stop"
				if err := reservation.Stop(rec.ID); err != nil {
					return err
				}
			}
			phase = "save"
			if err := mutation.UpdateService(r.Context(), rec); err != nil {
				return err
			}
			var err error
			authoritative, err = mutation.ListAllServices(r.Context())
			return err
		})
		if err != nil {
			return err
		}
		saved = true
		if reservation == nil {
			return nil
		}
		current := make([]*store.Service, 0, len(expected))
		for _, snapshot := range expected {
			for _, record := range authoritative {
				if record.ID == snapshot.ID {
					current = append(current, record)
					break
				}
			}
		}
		phase = "apply"
		applied := false
		defer func() {
			if !applied {
				reservation.Invalidate(rec.ID)
			}
		}()
		return a.applyServices(r, rec.OrgID, current, reservation, func() error {
			applied = true
			if restart {
				phase = "start"
				operationErr = reservation.Start(rec.ID, "")
			}
			return operationErr
		})
	})
	if err == nil {
		saved = true
		err = operationErr
	}
	if a.serviceOperationError(w, err, phase, saved) {
		return
	}
	a.audit(r, "service.update", rec.ID, rec.Name)
	a.respondService(w, r, http.StatusOK, rec)
}

func (a *API) deleteServiceHandler(w http.ResponseWriter, r *http.Request) {
	r, release, ok := a.beginServiceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	rec, ok := a.scopedService(w, r)
	if !ok {
		return
	}
	expected, definitions, ok := a.authorizeServiceGraph(w, r, nil, rec)
	if !ok {
		return
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
	dependents, err := a.data.ListServiceDependents(r.Context(), rec.OrgID, rec.ID)
	if a.handleErr(w, err) {
		return
	}
	if len(dependents) > 0 {
		ids := []string{}
		for _, dependent := range dependents {
			ids = append(ids, dependent.ID)
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "service is required by other services; remove their requirements first", "required_by": ids})
		return
	}
	deleted := false
	err = a.reserveService(r, definitions, func(reservation services.LifecycleReservation) error {
		err := a.snapshotMutation(r, rec.OrgID, expected, func(mutation store.ServiceMutation) error {
			if err := mutation.CheckDeleteService(r.Context(), rec.ID); err != nil {
				return err
			}
			if reservation != nil {
				if err := reservation.Stop(rec.ID); err != nil {
					return err
				}
			}
			return mutation.DeleteService(r.Context(), rec.ID)
		})
		if err != nil {
			return err
		}
		deleted = true
		if reservation == nil {
			return nil
		}
		reservation.Invalidate(rec.ID)
		remaining := []*store.Service{}
		for _, record := range expected {
			if record.ID != rec.ID {
				remaining = append(remaining, record)
			}
		}
		return a.applyServices(r, rec.OrgID, remaining, reservation, nil)
	})
	if a.serviceOperationError(w, err, "delete", deleted) {
		return
	}
	a.audit(r, "service.delete", rec.ID, rec.Name)
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
	r, release, ok := a.beginServiceMutation(w, r)
	if !ok {
		return
	}
	defer release()
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
	expected, definitions, ok := a.authorizeServiceGraph(w, r, nil, rec)
	if !ok {
		return
	}
	err = a.withServiceMutation(r, expected, definitions, func(reservation services.LifecycleReservation, _ store.ServiceMutation) error {
		return reservation.Start(rec.ID, ready)
	})
	if a.serviceOperationError(w, err, "start", false) {
		return
	}
	a.audit(r, "service.start", rec.ID, rec.Name)
	a.respondService(w, r, http.StatusOK, rec)
}

func (a *API) stopServiceHandler(w http.ResponseWriter, r *http.Request) {
	r, release, ok := a.beginServiceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	rec, ok := a.controllableService(w, r)
	if !ok {
		return
	}
	expected, definitions, ok := a.authorizeServiceGraph(w, r, nil, rec)
	if !ok {
		return
	}
	err := a.withServiceMutation(r, expected, definitions, func(reservation services.LifecycleReservation, _ store.ServiceMutation) error {
		return reservation.Stop(rec.ID)
	})
	if a.serviceOperationError(w, err, "stop", false) {
		return
	}
	a.audit(r, "service.stop", rec.ID, rec.Name)
	a.respondService(w, r, http.StatusOK, rec)
}
