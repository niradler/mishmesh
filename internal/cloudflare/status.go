package cloudflare

import (
	"sort"
	"time"
)

const (
	defaultDebounce = 2 * time.Second
	dnsComment      = "managed by mishmesh"

	DNSOK        = "ok"
	DNSMissing   = "missing"
	DNSConflict  = "conflict"
	DNSError     = "error"
	DNSUnchecked = "unchecked"

	AccessOK        = "ok"
	AccessMissing   = "missing"
	AccessNA        = "n/a"
	AccessError     = "error"
	AccessUnchecked = "unchecked"

	StatePublished = "published"
	StatePending   = "pending"
	StateConflict  = "conflict"
	StateUnclaimed = "unclaimed"
	StateError     = "error"

	TunnelHealthy  = "healthy"
	TunnelDown     = "down"
	TunnelUnknown  = "unknown"
	unclaimedOwned = "no endpoint claims this hostname"
	unclaimedOff   = "an endpoint serves this domain but its Public access is off"
	unclaimedFix   = "Open the endpoint and turn on Public access so mishmesh tracks this hostname."
)

type HostStatus struct {
	Host                string `json:"host"`
	EndpointID          string `json:"endpoint_id,omitempty"`
	Ingress             bool   `json:"ingress"`
	Service             string `json:"service,omitempty"`
	DNS                 string `json:"dns"`
	DNSDetail           string `json:"dns_detail,omitempty"`
	Access              string `json:"access"`
	AccessApp           string `json:"access_app,omitempty"`
	AccessPolicy        string `json:"access_policy,omitempty"`
	State               string `json:"state"`
	Detail              string `json:"detail,omitempty"`
	Fix                 string `json:"fix,omitempty"`
	AccessHostname      string `json:"-"`
	AccessHostnameKnown bool   `json:"-"`
}

type TunnelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Connections int    `json:"connections"`
}

type Status struct {
	Level      string            `json:"level"`
	Tunnel     *TunnelInfo       `json:"tunnel,omitempty"`
	Origin     string            `json:"origin,omitempty"`
	Connector  ConnectorStatus   `json:"connector"`
	Supervisor *SupervisorStatus `json:"supervisor,omitempty"`
	LastSync   time.Time         `json:"last_sync,omitzero"`
	LastError  string            `json:"last_error,omitempty"`
	Hostnames  []HostStatus      `json:"hostnames"`
}

func connectorTunnelHealth(c ConnectorStatus) string {
	switch {
	case !c.Reachable:
		return TunnelUnknown
	case c.ReadyConnections > 0:
		return TunnelHealthy
	default:
		return TunnelDown
	}
}

func unclaimedRoutes(routes map[string]string, wanted map[string]bool, private map[string]string, originKey, dns, access string) []HostStatus {
	var out []HostStatus
	for h, service := range routes {
		if wanted[h] {
			continue
		}
		hs := HostStatus{Host: h, Ingress: true, Service: service, DNS: dns, Access: access, State: StateUnclaimed, Detail: unclaimedOwned}
		if endpointID, known := private[h]; known {
			hs.EndpointID = endpointID
		}
		switch endpointID, known := private[h]; {
		case normalizeService(service) != originKey:
			hs.Detail = "routed to " + service + ", not through mishmesh"
		case known:
			hs.EndpointID, hs.Detail, hs.Fix = endpointID, unclaimedOff, unclaimedFix
		}
		out = append(out, hs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

type hostEntry struct {
	HostStatus
	orphan         bool
	conflict       bool
	failed         bool
	accessApplied  bool
	accessReleased bool
	own            *ownAccess
}

func (p *Publisher) describeAccess(e *hostEntry) {
	switch {
	case e.own != nil:
		e.AccessApp, e.AccessPolicy, e.Access = AccessAppOwn, e.own.status, AccessOK
		if e.own.status == AccessPolicyError {
			e.Access = AccessError
			if e.Detail == "" {
				e.Detail = e.own.detail
			}
		}
	case p.cfg.AccessAppID != "":
		e.AccessApp = AccessAppShared
	}
}

func entryState(e *hostEntry) string {
	if e.orphan {
		if e.failed || e.DNS == DNSError || e.Access == AccessError {
			return StateError
		}
		return StateUnclaimed
	}
	switch {
	case e.failed || e.DNS == DNSError || e.Access == AccessError:
		return StateError
	case e.conflict || e.DNS == DNSConflict:
		return StateConflict
	case !e.Ingress || e.DNS == DNSMissing || e.Access == AccessMissing:
		return StatePending
	default:
		return StatePublished
	}
}
