package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

const connectionOperationTimeout = 20 * time.Second

type connectionInput struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"config"`
	Secrets json.RawMessage `json:"secrets"`
}

type connectionDetectInput struct {
	Type  string          `json:"type"`
	Input json.RawMessage `json:"input"`
}

func decodeConnectionJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	object := json.NewDecoder(bytes.NewReader(raw))
	token, err := object.Token()
	if err != nil || token != json.Delim('{') {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	keys := map[string]bool{}
	for object.More() {
		token, err := object.Token()
		key, ok := token.(string)
		var field json.RawMessage
		if err != nil || !ok || keys[key] || object.Decode(&field) != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return false
		}
		keys[key] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (a *API) connectionTypesHandler(w http.ResponseWriter, _ *http.Request) {
	descriptors := []connections.Descriptor{}
	if a.connectionRegistry != nil {
		descriptors = a.connectionRegistry.Descriptors()
	}
	writeJSON(w, http.StatusOK, descriptors)
}

func (a *API) connectionProvider(w http.ResponseWriter, kind string) (connections.Provider, connections.Descriptor, bool) {
	if a.connectionRegistry == nil {
		writeError(w, http.StatusServiceUnavailable, "connections are unavailable on this server")
		return nil, connections.Descriptor{}, false
	}
	provider, found := a.connectionRegistry.Lookup(kind)
	if !found {
		writeError(w, http.StatusBadRequest, "unknown connection type")
		return nil, connections.Descriptor{}, false
	}
	for _, descriptor := range a.connectionRegistry.Descriptors() {
		if descriptor.Type == kind {
			return provider, descriptor, true
		}
	}
	writeError(w, http.StatusBadRequest, "unknown connection type")
	return nil, connections.Descriptor{}, false
}

func (a *API) scopedConnection(w http.ResponseWriter, r *http.Request) (*store.Connection, bool) {
	record, err := a.data.GetConnection(r.Context(), r.PathValue("id"))
	if a.handleErr(w, err) {
		return nil, false
	}
	if record.OrgID != a.orgScope(r) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return record, true
}

func connectionFields(descriptor connections.Descriptor, secrets bool) []typedfields.Field {
	fields := []typedfields.Field{}
	for _, field := range descriptor.Fields {
		if (field.Kind == "secret") == secrets {
			fields = append(fields, field)
		}
	}
	return fields
}

func (a *API) connectionView(record *store.Connection, status connections.Status) (map[string]any, error) {
	cfg, err := connections.OpenConfig(*record, a.secretBox, a.hostCommands)
	if err != nil {
		return nil, err
	}
	descriptor := connections.Descriptor{}
	if a.connectionRegistry != nil {
		for _, candidate := range a.connectionRegistry.Descriptors() {
			if candidate.Type == record.Type {
				descriptor = candidate
				break
			}
		}
	}
	safe, err := connections.SanitizeJSON(cfg, descriptor, record.Config)
	if err != nil {
		return nil, err
	}
	rawName, err := json.Marshal(record.Name)
	if err != nil {
		return nil, err
	}
	safeName, err := connections.SanitizeJSON(cfg, descriptor, rawName)
	if err != nil {
		return nil, err
	}
	view := map[string]any{"id": record.ID, "name": safeName, "type": record.Type, "config": safe, "status": connections.SanitizeStatus(cfg, descriptor, status)}
	if status.LastError != "" {
		view["action"] = "Retry sync, or save and restart the server after correcting the provider configuration."
	}
	for _, field := range connectionFields(descriptor, true) {
		view["has_"+field.Key] = cfg.Secrets[field.Key] != ""
	}
	return view, nil
}

func (a *API) connectionStatus(id string) connections.Status {
	if a.connections == nil {
		return connections.Status{Health: "down", LastError: "connection runtime unavailable; save and restart after enabling network control"}
	}
	return a.connections.Status(id)
}

func (a *API) writeConnection(w http.ResponseWriter, record *store.Connection, status connections.Status, code int) {
	view, err := a.connectionView(record, status)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, code, view)
}

func (a *API) listConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	records, err := a.data.ListConnections(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	views := make([]map[string]any, 0, len(records))
	for _, record := range records {
		view, err := a.connectionView(record, a.connectionStatus(record.ID))
		if a.handleErr(w, err) {
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}

func (a *API) getConnectionHandler(w http.ResponseWriter, r *http.Request) {
	if record, ok := a.scopedConnection(w, r); ok {
		a.writeConnection(w, record, a.connectionStatus(record.ID), http.StatusOK)
	}
}

func (a *API) connectionHostAccess(w http.ResponseWriter, r *http.Request, provider connections.Provider, descriptor connections.Descriptor, cfg connections.Config) bool {
	var values map[string]json.RawMessage
	err := json.Unmarshal(cfg.Record.Config, &values)
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	for key, value := range cfg.Secrets {
		raw, marshalError := json.Marshal(value)
		if marshalError != nil {
			err = marshalError
		}
		values[key] = raw
	}
	raw, marshalError := json.Marshal(values)
	if marshalError != nil {
		err = marshalError
	}
	usesCommands, validationError := typedfields.UsesHostCommands(descriptor.Fields, raw)
	if validationError != nil {
		err = validationError
	}
	if capability, ok := provider.(connections.HostCommandProvider); ok {
		usesCommands = usesCommands || capability.UsesHostCommands(connections.CloneConfig(cfg))
	}
	if err != nil || usesCommands {
		if err := a.hostAccess(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return false
		}
	}
	return true
}

func (a *API) saveConnection(w http.ResponseWriter, r *http.Request, existing *store.Connection) {
	if a.connections == nil || a.secretBox == nil {
		writeError(w, http.StatusServiceUnavailable, "connections are unavailable on this server")
		return
	}
	var input connectionInput
	if !decodeConnectionJSON(w, r, &input) {
		return
	}
	record := store.Connection{ID: store.NewID("conn"), OrgID: a.orgScope(r), Type: input.Type, CreatedAt: a.now()}
	values := map[string]string{}
	if existing != nil {
		record = *existing
		if input.Type != "" && input.Type != record.Type {
			writeError(w, http.StatusBadRequest, "connection type is immutable; create another connection to change type")
			return
		}
	}
	provider, descriptor, ok := a.connectionProvider(w, record.Type)
	if !ok {
		return
	}
	if existing != nil {
		old, err := connections.OpenConfig(*existing, a.secretBox, a.hostCommands)
		if a.handleErr(w, err) || !a.connectionHostAccess(w, r, provider, descriptor, old) {
			return
		}
		values = old.Secrets
	}
	record.Name = strings.TrimSpace(input.Name)
	if record.Name == "" || len(record.Name) > 100 {
		writeError(w, http.StatusBadRequest, "name is required (at most 100 characters)")
		return
	}
	if len(input.Config) == 0 {
		input.Config = json.RawMessage(`{}`)
	}
	if err := typedfields.Validate(connectionFields(descriptor, false), input.Config); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	record.Config = append(json.RawMessage(nil), input.Config...)
	if len(input.Secrets) > 0 {
		fields := connectionFields(descriptor, true)
		for i := range fields {
			fields[i].Required = false
		}
		if typedfields.Validate(fields, input.Secrets) != nil {
			writeError(w, http.StatusBadRequest, "invalid connection secrets")
			return
		}
		var supplied map[string]string
		if json.Unmarshal(input.Secrets, &supplied) != nil {
			writeError(w, http.StatusBadRequest, "invalid connection secrets")
			return
		}
		for key, value := range supplied {
			if len(value) > 16384 {
				writeError(w, http.StatusBadRequest, "connection secret is too long")
				return
			}
			values[key] = value
		}
	}
	plain, err := json.Marshal(values)
	if a.handleErr(w, err) {
		return
	}
	if err := typedfields.Validate(connectionFields(descriptor, true), plain); err != nil {
		writeError(w, http.StatusBadRequest, "required connection secret is missing")
		return
	}
	cfg := connections.Config{Record: record, Secrets: values, HostCommands: a.hostCommands}
	cfg.Record.SecretsEnc = nil
	if !a.connectionHostAccess(w, r, provider, descriptor, cfg) {
		return
	}
	if err := provider.Validate(connections.CloneConfig(cfg)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid connection configuration or secrets")
		return
	}
	record.SecretsEnc, err = a.secretBox.Seal(plain, connections.SecretContext(record.OrgID, record.ID))
	if a.handleErr(w, err) {
		return
	}
	record.UpdatedAt = a.now()
	code, action := http.StatusCreated, "connection.create"
	if existing == nil {
		err = a.data.CreateConnection(r.Context(), &record)
	} else {
		code, action = http.StatusOK, "connection.update"
		err = a.data.UpdateConnectionIfCurrent(r.Context(), &record, existing)
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "connection changed or name already exists in this organization; reload before retrying")
		return
	}
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, action, record.ID, record.Type)
	ctx, cancel := context.WithTimeout(r.Context(), connectionOperationTimeout)
	defer cancel()
	status := a.connectionStatus(record.ID)
	if err := a.connections.ReloadIfCurrent(ctx, record); err != nil {
		status.Health = "down"
		status.LastError = "saved connection could not reload; retry sync or save and restart"
	} else {
		status, err = a.connections.SyncIfCurrent(ctx, record)
		if err != nil {
			status.Health = "degraded"
			status.LastError = "saved connection could not sync; retry sync or save and restart"
		}
	}
	view, err := a.connectionView(&record, status)
	if a.handleErr(w, err) {
		return
	}
	if status.LastError != "" {
		view["action"] = "Connection saved. Retry sync, or save and restart the server."
	}
	writeJSON(w, code, view)
}

func (a *API) createConnectionHandler(w http.ResponseWriter, r *http.Request) {
	a.saveConnection(w, r, nil)
}

func (a *API) updateConnectionHandler(w http.ResponseWriter, r *http.Request) {
	if record, ok := a.scopedConnection(w, r); ok {
		a.saveConnection(w, r, record)
	}
}

func (a *API) deleteConnectionHandler(w http.ResponseWriter, r *http.Request) {
	record, ok := a.scopedConnection(w, r)
	if !ok {
		return
	}
	provider, descriptor, ok := a.connectionProvider(w, record.Type)
	if !ok {
		return
	}
	cfg, err := connections.OpenConfig(*record, a.secretBox, a.hostCommands)
	if a.handleErr(w, err) || !a.connectionHostAccess(w, r, provider, descriptor, cfg) {
		return
	}
	err = a.data.DeleteConnectionIfCurrent(r.Context(), record)
	var inUse *store.ConnectionInUseError
	if errors.As(err, &inUse) {
		raw, err := json.Marshal(map[string]any{"error": inUse.Error(), "references": inUse.References})
		if a.handleErr(w, err) {
			return
		}
		safe, err := connections.SanitizeJSON(cfg, descriptor, raw)
		if a.handleErr(w, err) {
			return
		}
		writeJSON(w, http.StatusConflict, safe)
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "connection changed; reload before retrying")
		return
	}
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "connection.delete", record.ID, record.Type)
	if a.connections != nil {
		ctx, cancel := context.WithTimeout(r.Context(), connectionOperationTimeout)
		defer cancel()
		if err := a.connections.Reload(ctx, record.ID); err != nil && !errors.Is(err, connections.ErrNotConnected) {
			a.log.Warn("connection runtime removal pending", "connection_id", record.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) syncConnectionHandler(w http.ResponseWriter, r *http.Request) {
	record, ok := a.scopedConnection(w, r)
	if !ok {
		return
	}
	provider, descriptor, ok := a.connectionProvider(w, record.Type)
	if !ok {
		return
	}
	cfg, err := connections.OpenConfig(*record, a.secretBox, a.hostCommands)
	if a.handleErr(w, err) || !a.connectionHostAccess(w, r, provider, descriptor, cfg) {
		return
	}
	if a.connections == nil {
		writeError(w, http.StatusServiceUnavailable, "connection runtime unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), connectionOperationTimeout)
	defer cancel()
	status, err := a.connections.SyncIfCurrent(ctx, *record)
	if errors.Is(err, connections.ErrNotConnected) {
		err = a.connections.ReloadIfCurrent(ctx, *record)
		if err == nil {
			status, err = a.connections.SyncIfCurrent(ctx, *record)
		}
	}
	a.audit(r, "connection.sync", record.ID, "")
	code := http.StatusOK
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "connection changed; reload its configuration and save before retrying sync")
		return
	}
	if err != nil {
		code = http.StatusBadGateway
		status.Health = "degraded"
		status.LastError = "sync failed; retry sync or save and restart"
	}
	a.writeConnection(w, record, status, code)
}

func (a *API) detectConnectionHandler(w http.ResponseWriter, r *http.Request) {
	var input connectionDetectInput
	if !decodeConnectionJSON(w, r, &input) {
		return
	}
	provider, descriptor, ok := a.connectionProvider(w, input.Type)
	if !ok {
		return
	}
	if !descriptor.Detect {
		writeError(w, http.StatusBadRequest, "connection type does not support Detect")
		return
	}
	if len(input.Input) == 0 {
		input.Input = json.RawMessage(`{}`)
	}
	fields := append([]typedfields.Field(nil), descriptor.Fields...)
	for i := range fields {
		fields[i].Required = false
	}
	if typedfields.Validate(fields, input.Input) != nil {
		writeError(w, http.StatusBadRequest, "invalid detection input")
		return
	}
	usesCommands, err := typedfields.UsesHostCommands(fields, input.Input)
	if err != nil || usesCommands || descriptor.DetectHostCommand {
		if err := a.hostAccess(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), connectionOperationTimeout)
	defer cancel()
	result, err := provider.Detect(ctx, append(json.RawMessage(nil), input.Input...))
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection detection failed; check the local provider configuration")
		return
	}
	var supplied map[string]json.RawMessage
	_ = json.Unmarshal(input.Input, &supplied)
	cfg := connections.Config{Record: store.Connection{Config: input.Input}, Secrets: map[string]string{}}
	for _, field := range connectionFields(descriptor, true) {
		var secret string
		_ = json.Unmarshal(supplied[field.Key], &secret)
		cfg.Secrets[field.Key] = secret
	}
	safe, err := connections.SanitizeJSON(cfg, descriptor, result)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid connection detection response")
		return
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(safe))
	decoder.UseNumber()
	if decoder.Decode(&object) != nil || object == nil {
		writeError(w, http.StatusBadGateway, "invalid connection detection response")
		return
	}
	writeJSON(w, http.StatusOK, importedDetectionLevels(object))
}

func importedDetectionLevels(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if key == "level" && item == "managed" {
				value[key] = "imported"
			} else {
				value[key] = importedDetectionLevels(item)
			}
		}
	case []any:
		for index, item := range value {
			value[index] = importedDetectionLevels(item)
		}
	}
	return value
}
