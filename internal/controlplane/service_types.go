package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

func (a *API) driverRegistry() *services.Registry {
	if a.serviceRegistry != nil {
		return a.serviceRegistry
	}
	return services.NewBuiltinRegistry()
}

func (a *API) lockServiceMutation(ctx context.Context) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.servicesMu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *API) serviceDriversHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.driverRegistry().Descriptors())
}

func preserveServiceSecrets(fields []typedfields.Field, proposed, existing *store.Service) error {
	optionalFields := append([]typedfields.Field(nil), fields...)
	for index := range optionalFields {
		optionalFields[index].Required = false
	}
	if err := typedfields.Validate(optionalFields, proposed.Config); err != nil {
		return err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(proposed.Config, &values); err != nil || values == nil {
		return errors.New("config must be an object")
	}
	previous := map[string]json.RawMessage{}
	if existing != nil && existing.Driver == proposed.Driver {
		_ = json.Unmarshal(existing.Config, &previous)
	}
	for _, field := range fields {
		if !field.Sensitive && field.Kind != "secret" {
			continue
		}
		value, supplied := values[field.Key]
		old, retained := previous[field.Key]
		if !supplied && retained {
			values[field.Key] = old
			value, supplied = old, true
		}
		if field.Kind == "keyvalue" && supplied {
			var entries, oldEntries map[string]string
			if json.Unmarshal(value, &entries) != nil {
				continue
			}
			_ = json.Unmarshal(old, &oldEntries)
			for key, entry := range entries {
				if entry != redactedEnvValue {
					continue
				}
				previousEntry, exists := oldEntries[key]
				if !exists || previousEntry == redactedEnvValue {
					return errors.New("redacted values require an existing secret")
				}
				entries[key] = previousEntry
			}
			values[field.Key], _ = json.Marshal(entries)
			continue
		}
		if bytes.Equal(value, []byte(`"<redacted>"`)) {
			if !retained || bytes.Equal(old, value) {
				return errors.New("redacted values require an existing secret")
			}
			values[field.Key] = old
		}
	}
	var err error
	proposed.Config, err = json.Marshal(values)
	return err
}

func (a *API) authorizeServiceGraph(w http.ResponseWriter, r *http.Request, proposed, existing *store.Service) ([]*store.Service, []services.Definition, bool) {
	orgID := a.orgScope(r)
	records, err := a.data.ListServices(r.Context(), orgID)
	if a.handleErr(w, err) {
		return nil, nil, false
	}
	if proposed != nil {
		if err := services.ValidateRequirements(orgID, proposed.ID, proposed.Requires, records); err != nil {
			writeError(w, http.StatusBadRequest, "requires must reference an acyclic graph of services in this organization")
			return nil, nil, false
		}
	}
	byID := make(map[string]*store.Service, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	selected := map[string]bool{}
	expected := []*store.Service{}
	definitions := []services.Definition{}
	var walk func(*store.Service, bool) error
	walk = func(record *store.Service, persisted bool) error {
		if record == nil || record.OrgID != orgID {
			return errors.New("requires must reference services in this organization")
		}
		def, err := services.FromRecordWithRegistry(record, a.driverRegistry())
		if err != nil {
			return errors.New("invalid service definition")
		}
		driver, _ := a.driverRegistry().Lookup(def.Driver)
		host, err := typedfields.UsesHostCommands(driver.Describe().Fields, def.Config)
		if err != nil {
			return errors.New("invalid service config")
		}
		if host {
			if err := a.hostAccess(r); err != nil {
				return err
			}
		}
		if persisted {
			if selected[record.ID] {
				return nil
			}
			selected[record.ID] = true
			expected = append(expected, record)
			definitions = append(definitions, def)
		}
		for _, id := range record.Requires {
			if err := walk(byID[id], true); err != nil {
				return err
			}
		}
		return nil
	}
	for _, candidate := range []struct {
		record    *store.Service
		persisted bool
	}{{existing, true}, {proposed, false}} {
		if candidate.record == nil {
			continue
		}
		if err := walk(candidate.record, candidate.persisted); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "permission denied") || errors.Is(err, errHostCommandsOff) {
				status = http.StatusForbidden
			}
			writeError(w, status, err.Error())
			return nil, nil, false
		}
	}
	return expected, definitions, true
}

func (a *API) serviceOperationError(w http.ResponseWriter, err error, phase string, saved bool) bool {
	if err == nil {
		return false
	}
	var held *services.RequirementInUseError
	if errors.As(err, &held) {
		dependents := make([]map[string]string, 0, len(held.Dependents))
		for _, dependent := range held.Dependents {
			dependents = append(dependents, map[string]string{"id": dependent.ID, "name": dependent.Name, "state": dependent.State})
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": held.Error(), "dependents": dependents, "phase": phase, "saved": saved})
		return true
	}
	status := http.StatusBadGateway
	message := "service operation failed"
	if errors.Is(err, store.ErrConflict) || errors.Is(err, services.ErrDefinitionChanged) || errors.Is(err, services.ErrRestartRequired) {
		status, message = http.StatusConflict, "service definition changed; reload and retry"
		if errors.Is(err, services.ErrRestartRequired) {
			message = services.ErrRestartRequired.Error()
		}
	}
	writeJSON(w, status, map[string]any{"error": message, "phase": phase, "saved": saved})
	return true
}

func (a *API) serviceDefinitions(ctx context.Context, mutation store.ServiceMutation, records []*store.Service) ([]services.Definition, error) {
	definitions := make([]services.Definition, 0, len(records))
	fallbacks := map[string]map[string]string{}
	for _, record := range records {
		definition, err := services.FromRecordWithRegistry(record, a.driverRegistry())
		if err != nil {
			a.log.Error("service skipped", "id", record.ID, "org", record.OrgID)
			continue
		}
		if fallbacks[record.OrgID] == nil {
			endpoints, err := mutation.ListEndpointsByOrg(ctx, record.OrgID)
			if err != nil {
				return nil, err
			}
			fallbacks[record.OrgID] = map[string]string{}
			for _, endpoint := range endpoints {
				if endpoint.Policy != nil && len(endpoint.Policy.ProxyTargets) > 0 && fallbacks[record.OrgID][endpoint.Policy.ServiceID] == "" {
					fallbacks[record.OrgID][endpoint.Policy.ServiceID] = endpoint.Policy.ProxyTargets[0]
				}
			}
		}
		definition.FallbackReadyAddr = fallbacks[record.OrgID][record.ID]
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func (a *API) reserveService(r *http.Request, definitions []services.Definition, action func(services.LifecycleReservation) error) error {
	if a.services == nil {
		return action(nil)
	}
	return a.services.Reserve(r.Context(), definitions, action)
}

func (a *API) withServiceMutation(r *http.Request, expected []*store.Service, definitions []services.Definition, action func(services.LifecycleReservation, store.ServiceMutation) error) error {
	snapshots, ok := a.data.(store.ServiceSnapshotStore)
	if !ok {
		return errors.New("service snapshot transactions are unavailable")
	}
	lease := func(reservation services.LifecycleReservation) error {
		return snapshots.WithServiceSnapshots(r.Context(), a.orgScope(r), expected, func(mutation store.ServiceMutation) error {
			return action(reservation, mutation)
		})
	}
	if a.services == nil {
		return lease(nil)
	}
	return a.services.Reserve(r.Context(), definitions, lease)
}
