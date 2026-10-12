package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

type ServiceControl interface {
	VisibleTo(orgID, name string) bool
	List() []services.Status
	Status(name string) (services.Status, bool)
	EnsureRunning(ctx context.Context, name, readyAddr string) error
	Stop(ctx context.Context, name string) error
}

type HealthSource interface {
	Get(endpointID string) (health.EndpointHealth, bool)
	ProbeNow(ctx context.Context, endpointID string) (health.EndpointHealth, error)
	Forget(endpointID string)
}

type FrontDoorControl interface {
	Enabled(orgID string) bool
	Status() cloudflare.Status
	Sync(ctx context.Context) (cloudflare.Status, error)
	Trigger()
}

func (a *API) SetServices(s ServiceControl) {
	a.services = s
}

func (a *API) SetHealth(h HealthSource) {
	a.endpointHealth = h
}

func (a *API) SetFrontDoor(f FrontDoorControl) {
	a.frontDoor = f
}

const healthProbeTimeout = 10 * time.Second

func (a *API) endpointChanged(ep *store.Endpoint) {
	if a.frontDoor != nil {
		a.frontDoor.Trigger()
	}
	if a.endpointHealth == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
		defer cancel()
		if _, err := a.endpointHealth.ProbeNow(ctx, ep.ID); err != nil {
			a.log.Warn("health probe after endpoint change failed", "endpoint_id", ep.ID, "err", err)
		}
	}()
}

func (a *API) endpointRemoved(ep *store.Endpoint) {
	if a.endpointHealth != nil {
		a.endpointHealth.Forget(ep.ID)
	}
	if a.frontDoor != nil {
		a.frontDoor.Trigger()
	}
}

func (a *API) validateNetworkPolicy(orgID, domain string, pol *store.EndpointPolicy) error {
	if pol == nil {
		return nil
	}
	if pol.Service != "" {
		if a.services == nil {
			return errors.New("service: no services are configured on this server (MISHMESH_SERVICES_FILE)")
		}
		if !a.services.VisibleTo(orgID, pol.Service) {
			return fmt.Errorf("service %q is not configured for this org", pol.Service)
		}
	}
	switch pol.FrontDoor {
	case "":
	case store.FrontDoorCloudflare:
		if a.frontDoor == nil || !a.frontDoor.Enabled(orgID) {
			return errors.New("front_door cloudflare is not enabled for this org (MISHMESH_CLOUDFLARE_*)")
		}
		if domain == "" {
			return errors.New("front_door cloudflare requires a custom domain on the endpoint")
		}
	default:
		return fmt.Errorf("front_door must be empty or %q", store.FrontDoorCloudflare)
	}
	return nil
}

type serviceDTO struct {
	Name                string    `json:"name"`
	Driver              string    `json:"driver"`
	State               string    `json:"state"`
	Active              int       `json:"active"`
	LastActivity        time.Time `json:"last_activity,omitzero"`
	LastError           string    `json:"last_error,omitempty"`
	StartedBy           string    `json:"started_by,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
	IdleTimeoutSeconds  int       `json:"idle_timeout_seconds"`
	StartTimeoutSeconds int       `json:"start_timeout_seconds"`
	Endpoints           []string  `json:"endpoints"`
}

func (a *API) orgServiceEndpoints(ctx context.Context, orgID string) (map[string][]*store.Endpoint, error) {
	eps, err := a.data.ListEndpointsByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]*store.Endpoint)
	for _, ep := range eps {
		if name := endpointService(ep); name != "" {
			out[name] = append(out[name], ep)
		}
	}
	return out, nil
}

func endpointService(ep *store.Endpoint) string {
	if ep.Policy == nil {
		return ""
	}
	return ep.Policy.Service
}

func toServiceDTO(st services.Status, eps []*store.Endpoint) serviceDTO {
	ids := make([]string, 0, len(eps))
	for _, ep := range eps {
		ids = append(ids, ep.ID)
	}
	return serviceDTO{
		Name: st.Name, Driver: st.Driver, State: st.State, Active: st.Active,
		LastActivity: st.LastActivity, LastError: st.LastError, StartedBy: st.StartedBy, UpdatedAt: st.UpdatedAt,
		IdleTimeoutSeconds:  int(st.IdleTimeout / time.Second),
		StartTimeoutSeconds: int(st.StartTimeout / time.Second),
		Endpoints:           ids,
	}
}

func (a *API) listServicesHandler(w http.ResponseWriter, r *http.Request) {
	out := []serviceDTO{}
	if a.services == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	orgID := a.orgScope(r)
	byService, err := a.orgServiceEndpoints(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	for _, st := range a.services.List() {
		if a.services.VisibleTo(orgID, st.Name) {
			out = append(out, toServiceDTO(st, byService[st.Name]))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) visibleService(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if a.services == nil || !a.services.VisibleTo(a.orgScope(r), name) {
		writeError(w, http.StatusNotFound, "not found")
		return "", false
	}
	return name, true
}

func (a *API) writeServiceStatus(w http.ResponseWriter, r *http.Request, name string) {
	byService, err := a.orgServiceEndpoints(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	st, _ := a.services.Status(name)
	writeJSON(w, http.StatusOK, toServiceDTO(st, byService[name]))
}

func (a *API) startServiceHandler(w http.ResponseWriter, r *http.Request) {
	name, ok := a.visibleService(w, r)
	if !ok {
		return
	}
	byService, err := a.orgServiceEndpoints(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	ready := ""
	for _, ep := range byService[name] {
		if ep.Policy != nil && len(ep.Policy.ProxyTargets) > 0 {
			ready = ep.Policy.ProxyTargets[0]
			break
		}
	}
	if err := a.services.EnsureRunning(r.Context(), name, ready); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "service.start", name, "")
	a.writeServiceStatus(w, r, name)
}

func (a *API) stopServiceHandler(w http.ResponseWriter, r *http.Request) {
	name, ok := a.visibleService(w, r)
	if !ok {
		return
	}
	if err := a.services.Stop(r.Context(), name); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "service.stop", name, "")
	a.writeServiceStatus(w, r, name)
}

type frontDoorsDTO struct {
	Cloudflare cloudflare.Status `json:"cloudflare"`
}

func (a *API) frontDoorStatus(ctx context.Context, orgID string) (cloudflare.Status, error) {
	if a.frontDoor == nil || !a.frontDoor.Enabled(orgID) {
		return cloudflare.Status{Hostnames: []cloudflare.HostStatus{}}, nil
	}
	return a.scopeFrontDoor(ctx, orgID, a.frontDoor.Status())
}

func (a *API) scopeFrontDoor(ctx context.Context, orgID string, st cloudflare.Status) (cloudflare.Status, error) {
	eps, err := a.data.ListEndpointsByOrg(ctx, orgID)
	if err != nil {
		return cloudflare.Status{}, err
	}
	owned := make(map[string]bool, len(eps))
	for _, ep := range eps {
		owned[ep.ID] = true
	}
	hosts := make([]cloudflare.HostStatus, 0, len(st.Hostnames))
	for _, h := range st.Hostnames {
		if owned[h.EndpointID] {
			hosts = append(hosts, h)
		}
	}
	st.Hostnames = hosts
	return st, nil
}

func (a *API) listFrontDoorsHandler(w http.ResponseWriter, r *http.Request) {
	st, err := a.frontDoorStatus(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, frontDoorsDTO{Cloudflare: st})
}

func (a *API) syncCloudflareHandler(w http.ResponseWriter, r *http.Request) {
	if a.frontDoor == nil || !a.frontDoor.Enabled(a.orgScope(r)) {
		writeError(w, http.StatusNotFound, "cloudflare front door is not enabled for this org")
		return
	}
	st, err := a.frontDoor.Sync(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "frontdoor.sync", store.FrontDoorCloudflare, "")
	st, err = a.scopeFrontDoor(r.Context(), a.orgScope(r), st)
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, frontDoorsDTO{Cloudflare: st})
}
