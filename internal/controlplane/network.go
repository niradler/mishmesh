package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connections"
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
	Reserve(context.Context, []services.Definition, func(services.LifecycleReservation) error) error
}

type HealthSource interface {
	Get(endpointID string) (health.EndpointHealth, bool)
	ProbeNow(ctx context.Context, endpointID string) (health.EndpointHealth, error)
	Forget(endpointID string)
}

type ConnectionControl interface {
	Status(connectionID string) connections.Status
	Reload(ctx context.Context, connectionID string) error
	ReloadIfCurrent(ctx context.Context, expected store.Connection) error
	Sync(ctx context.Context, connectionID string) (connections.Status, error)
	SyncIfCurrent(ctx context.Context, expected store.Connection) (connections.Status, error)
	Trigger(connectionID string)
	AddHostnameIfCurrent(ctx context.Context, expected store.Connection, host, service string) error
	RemoveHostnameIfCurrent(ctx context.Context, expected store.Connection, host string) error
}

type ConnectionSnapshotStatus interface {
	StatusIfCurrent(expected store.Connection) connections.Status
}

func (a *API) SetServices(s ServiceControl) {
	a.services = s
	if a.serviceRegistry == nil {
		a.serviceRegistry = services.NewBuiltinRegistry()
	}
}

func (a *API) SetServiceRegistry(registry *services.Registry) {
	a.serviceRegistry = registry
}

func (a *API) SetIngressInvalidator(invalidate func()) { a.invalidateIngress = invalidate }

func (a *API) invalidateIngressCaches(orgID string) {
	if a.invalidateIngress != nil {
		a.invalidateIngress()
	}
	a.flushDecisions(orgID)
}

func (a *API) SetHealth(h HealthSource) {
	a.endpointHealth = h
}

func (a *API) SetConnections(hub ConnectionControl, registry *connections.Registry, box *secrets.Box) {
	a.connections = hub
	a.connectionRegistry = registry
	a.secretBox = box
}

func (a *API) SetHostCommands(enabled bool) {
	a.hostCommands = enabled
}

var errHostCommandsOff = errors.New("host commands are disabled on this server (MISHMESH_HOST_COMMANDS)")

type permissionDeniedError struct{ action authz.Action }

func (e *permissionDeniedError) Error() string { return "permission denied: " + string(e.action) }

func (a *API) hostAccess(r *http.Request) error {
	if !a.hostCommands {
		return errHostCommandsOff
	}
	if !a.permitted(r, authz.ActionHostExec) {
		return &permissionDeniedError{action: authz.ActionHostExec}
	}
	return nil
}

func (a *API) capabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"host_commands": a.hostAccess(r) == nil})
}

const healthProbeTimeout = 10 * time.Second

func (a *API) endpointChanged(ep *store.Endpoint) {
	a.invalidateIngressCaches(ep.OrgID)
	a.triggerConnections(ep.OrgID)
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
	a.invalidateIngressCaches(ep.OrgID)
	if a.endpointHealth != nil {
		a.endpointHealth.Forget(ep.ID)
	}
	a.triggerConnections(ep.OrgID)
}

func (a *API) validateNetworkPolicy(ctx context.Context, orgID, endpointID, domain, kind string, pol *store.EndpointPolicy, previous []store.AccessRef, snapshots *[]*store.Connection) error {
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
	if err := connections.NormalizeAccessPolicy(pol.AccessPolicy); err != nil {
		return err
	}
	if pol.AccessPolicy != nil && len(pol.Access) == 0 {
		return errors.New("access_policy needs at least one access connection")
	}
	seenConnections, seenHosts := map[string]bool{}, map[string]bool{}
	for index, ref := range pol.Access {
		if err := connections.NormalizeAccessPolicy(ref.Policy); err != nil {
			return err
		}
		if a.connectionRegistry == nil {
			return errors.New("access needs a registered connection provider")
		}
		record, err := a.data.GetConnection(ctx, ref.ConnectionID)
		if err != nil || record.OrgID != orgID {
			return errors.New("access must reference a connection in this organization")
		}
		*snapshots = append(*snapshots, record)
		provider, found := a.connectionRegistry.Lookup(record.Type)
		if !found {
			return errors.New("access needs a registered connection provider")
		}
		hostname, err := a.connectionRegistry.AccessHostname(record.Type, domain, ref.Hostname, kind)
		if err != nil {
			return err
		}
		if seenConnections[record.ID] || seenHosts[hostname] {
			return errors.New("access connections and hostnames must be unique")
		}
		seenConnections[record.ID], seenHosts[hostname] = true, true
		ref.Hostname = hostname
		pol.Access[index] = ref
		unchanged := slices.ContainsFunc(previous, func(before store.AccessRef) bool {
			return before.ConnectionID == ref.ConnectionID && before.Hostname == hostname
		})
		if !unchanged {
			cfg, err := connections.OpenConfig(*record, a.secretBox, a.hostCommands)
			if err != nil || provider.Validate(cfg) != nil {
				return errors.New("new or changed access needs a valid connection configuration")
			}
		}
		if pol.EffectiveAccessPolicy(ref) != nil {
			if err := validateAccessPolicySupport(provider, *record, a.secretBox, a.hostCommands); err != nil {
				return err
			}
		}
		for _, descriptor := range a.connectionRegistry.Descriptors() {
			if descriptor.Type == record.Type && descriptor.Scope == "public" {
				if _, status, message := a.authorizeDomainBinding(ctx, orgID, hostname); status != 0 {
					return errors.New(message)
				}
			}
		}
	}
	return nil
}

func validateAccessPolicySupport(provider connections.Provider, record store.Connection, box *secrets.Box, hostCommands bool) error {
	enforcer, supported := provider.(connections.AccessPolicyProvider)
	if !supported {
		return fmt.Errorf("%s connections do not support access policies", record.Type)
	}
	cfg, err := connections.OpenConfig(record, box, hostCommands)
	if err != nil {
		return errors.New("access policy needs a valid connection configuration")
	}
	return enforcer.ValidateAccessPolicy(cfg)
}

func (a *API) previousAccess(ctx context.Context, endpoint *store.Endpoint) []store.AccessRef {
	if endpoint.Policy == nil {
		return nil
	}
	previous := append([]store.AccessRef(nil), endpoint.Policy.Access...)
	for index, ref := range previous {
		if a.connectionRegistry == nil {
			continue
		}
		record, err := a.data.GetConnection(ctx, ref.ConnectionID)
		if err != nil || record.OrgID != endpoint.OrgID {
			continue
		}
		if hostname, err := a.connectionRegistry.AccessHostname(record.Type, endpoint.Domain, ref.Hostname, endpoint.Kind); err == nil {
			previous[index].Hostname = hostname
		}
	}
	return previous
}

func endpointServiceID(ep *store.Endpoint) string {
	if ep.Policy == nil {
		return ""
	}
	return ep.Policy.ServiceID
}

func (a *API) triggerConnections(orgID string) {
	if a.connections == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()
	records, err := a.data.ListConnections(ctx, orgID)
	if err != nil {
		a.log.Warn("list connections for endpoint change failed", "org", orgID)
		return
	}
	for _, record := range records {
		a.connections.Trigger(record.ID)
	}
}
