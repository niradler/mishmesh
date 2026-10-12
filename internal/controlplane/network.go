package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

type ServiceControl interface {
	Apply(defs []services.Definition)
	Status(id string) (services.Status, bool)
	RunningDependents(id string) []services.Status
	EnsureRunning(ctx context.Context, id, readyAddr string) error
	Stop(ctx context.Context, id string) error
}

type HealthSource interface {
	Get(endpointID string) (health.EndpointHealth, bool)
	ProbeNow(ctx context.Context, endpointID string) (health.EndpointHealth, error)
	Forget(endpointID string)
}

type CloudflareControl interface {
	DefaultOrigin() string
	Status(orgID string) cloudflare.Status
	Reload(ctx context.Context, orgID string) error
	Sync(ctx context.Context, orgID string) (cloudflare.Status, error)
	Trigger(orgID string)
}

type cloudflareDetector func(ctx context.Context, metricsAddr, tunnelToken string) (cloudflare.DetectResult, error)

func (a *API) SetServices(s ServiceControl) {
	a.services = s
}

func (a *API) SetHealth(h HealthSource) {
	a.endpointHealth = h
}

func (a *API) SetCloudflare(c CloudflareControl, box *secrets.Box) {
	a.cloudflare = c
	a.secretBox = box
	a.detectCloudflare = cloudflare.Detect
}

func (a *API) SetHostCommands(enabled bool) {
	a.hostCommands = enabled
}

var errHostCommandsOff = errors.New("host commands are disabled on this server (MISHMESH_HOST_COMMANDS)")

func (a *API) hostAccess(r *http.Request) error {
	if !a.hostCommands {
		return errHostCommandsOff
	}
	if !a.permitted(r, authz.ActionHostExec) {
		return errors.New("permission denied: " + string(authz.ActionHostExec))
	}
	return nil
}

func (a *API) capabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"host_commands": a.hostAccess(r) == nil})
}

const healthProbeTimeout = 10 * time.Second

func (a *API) endpointChanged(ep *store.Endpoint) {
	if a.cloudflare != nil {
		a.cloudflare.Trigger(ep.OrgID)
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
	if a.cloudflare != nil {
		a.cloudflare.Trigger(ep.OrgID)
	}
}

func (a *API) validateNetworkPolicy(ctx context.Context, orgID, domain string, pol *store.EndpointPolicy, wasPublic bool) error {
	if pol == nil {
		return nil
	}
	if pol.ServiceID != "" {
		svc, err := a.data.GetService(ctx, pol.ServiceID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && svc.OrgID != orgID) {
			return fmt.Errorf("service_id %q is not a service of this org", pol.ServiceID)
		}
		if err != nil {
			return fmt.Errorf("service_id: %w", err)
		}
	}
	switch pol.PublicAccess {
	case "":
	case store.PublicAccessCloudflare:
		if !wasPublic {
			if _, err := a.data.GetCloudflareConnection(ctx, orgID); errors.Is(err, store.ErrNotFound) {
				return errors.New("public_access cloudflare needs a Cloudflare connection for this org (PUT /cloudflare)")
			} else if err != nil {
				return fmt.Errorf("public_access: %w", err)
			}
		}
		if domain == "" {
			return errors.New("public_access cloudflare requires a custom domain on the endpoint")
		}
	default:
		return fmt.Errorf("public_access must be empty or %q", store.PublicAccessCloudflare)
	}
	return nil
}

func endpointServiceID(ep *store.Endpoint) string {
	if ep.Policy == nil {
		return ""
	}
	return ep.Policy.ServiceID
}

func endpointPublic(ep *store.Endpoint) bool {
	return ep.Policy != nil && ep.Policy.PublicAccess == store.PublicAccessCloudflare && ep.Domain != ""
}
