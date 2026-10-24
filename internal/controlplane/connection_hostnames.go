package controlplane

import (
	"context"
	"errors"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
)

type connectionHostnameInput struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
}

func (a *API) addConnectionHostnameHandler(w http.ResponseWriter, r *http.Request) {
	var input connectionHostnameInput
	if !decodeConnectionJSON(w, r, &input) {
		return
	}
	a.changeConnectionHostname(w, r, "connection.hostname.add", input.Hostname, http.StatusCreated, func(ctx context.Context, record store.Connection) error {
		return a.connections.AddHostnameIfCurrent(ctx, record, input.Hostname, input.Service)
	})
}

func (a *API) removeConnectionHostnameHandler(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("hostname")
	a.changeConnectionHostname(w, r, "connection.hostname.remove", host, http.StatusOK, func(ctx context.Context, record store.Connection) error {
		return a.connections.RemoveHostnameIfCurrent(ctx, record, host)
	})
}

func (a *API) changeConnectionHostname(w http.ResponseWriter, r *http.Request, action, host string, code int, change func(context.Context, store.Connection) error) {
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
	if !a.writeHostnameError(w, record.ID, change(ctx, *record)) {
		return
	}
	a.audit(r, action, record.ID, host)
	status, err := a.connections.SyncIfCurrent(ctx, *record)
	if err != nil {
		status.Health = "degraded"
		status = connections.WithHostDiagnostic(status, connections.HostSyncRetry)
	}
	a.writeConnection(w, record, status, code)
}

func (a *API) writeHostnameError(w http.ResponseWriter, connectionID string, err error) bool {
	var hostnameErr *connections.HostnameError
	switch {
	case err == nil:
		return true
	case errors.As(err, &hostnameErr):
		codes := map[connections.HostnameErrorKind]int{
			connections.HostnameInvalid:     http.StatusBadRequest,
			connections.HostnameConflict:    http.StatusConflict,
			connections.HostnameInUse:       http.StatusConflict,
			connections.HostnameNotFound:    http.StatusNotFound,
			connections.HostnameUnsupported: http.StatusConflict,
		}
		body := map[string]any{"error": hostnameErr.Message, "reason": hostnameErr.Kind}
		if len(hostnameErr.EndpointIDs) > 0 {
			body["endpoint_ids"] = hostnameErr.EndpointIDs
		}
		writeJSON(w, codes[hostnameErr.Kind], body)
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "connection changed; reload before retrying")
	case errors.Is(err, connections.ErrNotConnected):
		writeError(w, http.StatusServiceUnavailable, "connection is not running; save or sync it first")
	default:
		a.log.Warn("connection hostname change failed", "connection_id", connectionID, "error", err)
		writeError(w, http.StatusBadGateway, "the provider rejected the hostname change; check the API token permissions and retry")
	}
	return false
}
