package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	cloudflareDetectTimeout   = 10 * time.Second
	cloudflareSaveSyncTimeout = 20 * time.Second
	maxCloudflareNameLen      = 100
	maxCloudflareTokenLen     = 4096
)

var cloudflareIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,64}$`)

type cloudflareInput struct {
	Name          string `json:"name"`
	TunnelToken   string `json:"tunnel_token"`
	APIToken      string `json:"api_token"`
	ClearAPIToken bool   `json:"clear_api_token"`
	AccessAppID   string `json:"access_app_id"`
	Origin        string `json:"origin"`
	Connector     string `json:"connector"`
	MetricsAddr   string `json:"metrics_addr"`
	Prune         bool   `json:"prune"`
}

type cloudflareConnectionDTO struct {
	Name           string    `json:"name"`
	AccountID      string    `json:"account_id"`
	TunnelID       string    `json:"tunnel_id"`
	AccessAppID    string    `json:"access_app_id,omitempty"`
	Origin         string    `json:"origin"`
	Connector      string    `json:"connector"`
	MetricsAddr    string    `json:"metrics_addr"`
	Prune          bool      `json:"prune"`
	APITokenSet    bool      `json:"api_token_set"`
	TunnelTokenSet bool      `json:"tunnel_token_set"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type cloudflareDTO struct {
	Level      string                   `json:"level"`
	Connection *cloudflareConnectionDTO `json:"connection,omitempty"`
	Status     cloudflare.Status        `json:"status"`
}

type cloudflareDetectInput struct {
	MetricsAddr string `json:"metrics_addr"`
	TunnelToken string `json:"tunnel_token"`
}

func emptyCloudflareStatus() cloudflare.Status {
	return cloudflare.Status{Level: cloudflare.LevelNone, Connector: cloudflare.ConnectorStatus{EdgeLocations: []string{}}, Hostnames: []cloudflare.HostStatus{}}
}

func (a *API) cloudflareStatus(orgID string) cloudflare.Status {
	if a.cloudflare == nil {
		return emptyCloudflareStatus()
	}
	return a.cloudflare.Status(orgID)
}

func (a *API) defaultCloudflareOrigin() string {
	if a.cloudflare == nil {
		return ""
	}
	return a.cloudflare.DefaultOrigin()
}

func (a *API) cloudflareView(ctx context.Context, orgID string, st cloudflare.Status) (cloudflareDTO, error) {
	conn, err := a.data.GetCloudflareConnection(ctx, orgID)
	if errors.Is(err, store.ErrNotFound) {
		return cloudflareDTO{Level: cloudflare.LevelNone, Status: emptyCloudflareStatus()}, nil
	}
	if err != nil {
		return cloudflareDTO{}, err
	}
	level := cloudflare.LevelImported
	if len(conn.APITokenEnc) > 0 {
		level = cloudflare.LevelManaged
	}
	origin := conn.Origin
	if origin == "" {
		origin = a.defaultCloudflareOrigin()
	}
	return cloudflareDTO{
		Level: level,
		Connection: &cloudflareConnectionDTO{
			Name: conn.Name, AccountID: conn.AccountID, TunnelID: conn.TunnelID, AccessAppID: conn.AccessAppID,
			Origin: origin, Connector: conn.Connector, MetricsAddr: conn.MetricsAddr, Prune: conn.Prune,
			APITokenSet: len(conn.APITokenEnc) > 0, TunnelTokenSet: len(conn.TunnelTokenEnc) > 0, UpdatedAt: conn.UpdatedAt,
		},
		Status: st,
	}, nil
}

func (a *API) writeCloudflare(w http.ResponseWriter, r *http.Request, st cloudflare.Status) {
	view, err := a.cloudflareView(r.Context(), a.orgScope(r), st)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *API) getCloudflareHandler(w http.ResponseWriter, r *http.Request) {
	a.writeCloudflare(w, r, a.cloudflareStatus(a.orgScope(r)))
}

func (a *API) cloudflareWritable(w http.ResponseWriter, r *http.Request) bool {
	if a.cloudflare == nil || a.secretBox == nil {
		writeError(w, http.StatusServiceUnavailable, "cloudflare connections are not available on this server (no secret key)")
		return false
	}
	if err := a.hostAccess(r); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

func validateOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("origin must be an http:// or https:// URL")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("origin must not contain credentials, a path, a query or a fragment")
	}
	return nil
}

func (a *API) sealCloudflare(orgID, kind, plain string) ([]byte, error) {
	sealed, err := a.secretBox.Seal([]byte(plain), cloudflare.TokenContext(orgID, kind))
	if err != nil {
		return nil, fmt.Errorf("seal %s: %w", kind, err)
	}
	return sealed, nil
}

func (a *API) buildCloudflareConnection(ctx context.Context, orgID string, in cloudflareInput, existing *store.CloudflareConnection) (*store.CloudflareConnection, int, error) {
	conn := &store.CloudflareConnection{OrgID: orgID}
	if existing != nil {
		*conn = *existing
	}
	conn.Name = strings.TrimSpace(in.Name)
	if conn.Name == "" || len(conn.Name) > maxCloudflareNameLen {
		return nil, http.StatusBadRequest, fmt.Errorf("name is required (at most %d characters)", maxCloudflareNameLen)
	}
	conn.Connector = strings.TrimSpace(in.Connector)
	if conn.Connector == "" {
		conn.Connector = cloudflare.ConnectorExternal
	}
	if conn.Connector != cloudflare.ConnectorExternal && conn.Connector != cloudflare.ConnectorManaged {
		return nil, http.StatusBadRequest, fmt.Errorf("connector must be %q or %q", cloudflare.ConnectorExternal, cloudflare.ConnectorManaged)
	}
	conn.MetricsAddr = strings.TrimSpace(in.MetricsAddr)
	if conn.MetricsAddr == "" {
		conn.MetricsAddr = cloudflare.DefaultMetricsAddr
	}
	if err := cloudflare.CheckLoopback(conn.MetricsAddr); err != nil {
		return nil, http.StatusBadRequest, err
	}
	conn.Origin = strings.TrimRight(strings.TrimSpace(in.Origin), "/")
	if conn.Origin != "" {
		if err := validateOrigin(conn.Origin); err != nil {
			return nil, http.StatusBadRequest, err
		}
	} else if a.defaultCloudflareOrigin() == "" {
		return nil, http.StatusBadRequest, errors.New("origin is required: this server has no default ingress address")
	}
	conn.AccessAppID = strings.TrimSpace(in.AccessAppID)
	if conn.AccessAppID != "" && !cloudflareIDPattern.MatchString(conn.AccessAppID) {
		return nil, http.StatusBadRequest, errors.New("access_app_id must be a Cloudflare Access application ID")
	}
	conn.Prune = in.Prune

	tunnelToken := strings.TrimSpace(in.TunnelToken)
	if len(tunnelToken) > maxCloudflareTokenLen || len(in.APIToken) > maxCloudflareTokenLen {
		return nil, http.StatusBadRequest, errors.New("token is too long")
	}
	if tunnelToken == "" && len(conn.TunnelTokenEnc) == 0 {
		return nil, http.StatusBadRequest, errors.New("tunnel_token is required")
	}
	if tunnelToken != "" {
		tok, err := cloudflare.DecodeTunnelToken(tunnelToken)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if !cloudflareIDPattern.MatchString(tok.AccountID) || !cloudflareIDPattern.MatchString(tok.TunnelID) {
			return nil, http.StatusBadRequest, errors.New("tunnel token has an invalid account or tunnel ID")
		}
		if conn.TunnelTokenEnc, err = a.sealCloudflare(orgID, cloudflare.TokenKindTunnel, tunnelToken); err != nil {
			return nil, http.StatusInternalServerError, err
		}
		conn.AccountID, conn.TunnelID = tok.AccountID, tok.TunnelID
	}
	switch apiToken := strings.TrimSpace(in.APIToken); {
	case in.ClearAPIToken && apiToken != "":
		return nil, http.StatusBadRequest, errors.New("api_token and clear_api_token are mutually exclusive")
	case in.ClearAPIToken:
		conn.APITokenEnc = nil
	case apiToken != "":
		sealed, err := a.sealCloudflare(orgID, cloudflare.TokenKindAPI, apiToken)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		conn.APITokenEnc = sealed
	}
	if len(conn.APITokenEnc) == 0 && conn.Prune {
		return nil, http.StatusBadRequest, errors.New("prune needs an API token (managed level)")
	}

	all, err := a.data.ListCloudflareConnections(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	for _, other := range all {
		if other.OrgID != orgID && other.TunnelID == conn.TunnelID {
			return nil, http.StatusConflict, errors.New("this tunnel is already connected to another organization")
		}
	}
	conn.UpdatedAt = a.now()
	return conn, 0, nil
}

func (a *API) putCloudflareHandler(w http.ResponseWriter, r *http.Request) {
	if !a.cloudflareWritable(w, r) {
		return
	}
	var in cloudflareInput
	if !decodeJSON(w, r, &in) {
		return
	}
	orgID := a.orgScope(r)
	existing, err := a.data.GetCloudflareConnection(r.Context(), orgID)
	if errors.Is(err, store.ErrNotFound) {
		existing, err = nil, nil
	}
	if a.handleErr(w, err) {
		return
	}
	conn, status, err := a.buildCloudflareConnection(r.Context(), orgID, in, existing)
	if err != nil {
		if status == http.StatusInternalServerError {
			a.handleErr(w, err)
			return
		}
		writeError(w, status, err.Error())
		return
	}
	if a.handleErr(w, a.data.SetCloudflareConnection(r.Context(), conn)) {
		return
	}
	action := "cloudflare.import"
	if existing != nil {
		action = "cloudflare.update"
	}
	a.audit(r, action, conn.TunnelID, conn.Name)
	if err := a.cloudflare.Reload(r.Context(), orgID); err != nil {
		a.log.Warn("cloudflare reload after save failed", "org", orgID, "err", err)
		a.writeCloudflare(w, r, a.cloudflare.Status(orgID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cloudflareSaveSyncTimeout)
	defer cancel()
	st, err := a.cloudflare.Sync(ctx, orgID)
	if err != nil {
		a.log.Warn("cloudflare sync after save failed", "org", orgID, "err", err)
	}
	a.writeCloudflare(w, r, st)
}

func (a *API) deleteCloudflareHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	conn, err := a.data.GetCloudflareConnection(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	if a.handleErr(w, a.data.DeleteCloudflareConnection(r.Context(), orgID)) {
		return
	}
	a.audit(r, "cloudflare.delete", conn.TunnelID, conn.Name)
	if a.cloudflare != nil {
		if err := a.cloudflare.Reload(r.Context(), orgID); err != nil {
			a.log.Warn("cloudflare reload after delete failed", "org", orgID, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) detectCloudflareHandler(w http.ResponseWriter, r *http.Request) {
	if !a.cloudflareWritable(w, r) {
		return
	}
	var in cloudflareDetectInput
	if !decodeJSON(w, r, &in) {
		return
	}
	addr := strings.TrimSpace(in.MetricsAddr)
	if addr == "" {
		addr = cloudflare.DefaultMetricsAddr
	}
	if err := cloudflare.CheckLoopback(addr); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.TunnelToken) > maxCloudflareTokenLen {
		writeError(w, http.StatusBadRequest, "token is too long")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cloudflareDetectTimeout)
	defer cancel()
	res, err := a.detectCloudflare(ctx, addr, in.TunnelToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *API) syncCloudflareHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	if a.cloudflare == nil {
		writeError(w, http.StatusNotFound, cloudflare.ErrNotConnected.Error())
		return
	}
	st, err := a.cloudflare.Sync(r.Context(), orgID)
	if errors.Is(err, cloudflare.ErrNotConnected) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	a.audit(r, "cloudflare.sync", orgID, "")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "status": st})
		return
	}
	a.writeCloudflare(w, r, st)
}
