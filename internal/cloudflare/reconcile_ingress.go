package cloudflare

import (
	"context"
	"errors"
	"fmt"
)

type ingressOutcome struct {
	plan          ingressPlan
	observedRules []IngressRule
	putErr        error
	errs          []error
}

func (p *Publisher) reconcile(ctx context.Context) (Status, error) {
	st := p.baseStatus(ctx)
	hosts, private, err := p.desired(ctx, &st)
	if err != nil {
		return st, err
	}

	var errs []error
	st.Tunnel = p.observeTunnel(ctx, st.Connector, &errs)

	tcfg, err := p.client.GetTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID)
	if err != nil {
		errs = append(errs, err)
		for _, h := range hosts {
			st.Hostnames = append(st.Hostnames, HostStatus{
				Host: h.Host, EndpointID: h.EndpointID, DNS: DNSError, DNSDetail: "tunnel configuration unavailable",
				Access: AccessNA, State: StateError, Detail: err.Error(),
			})
		}
		joined := errors.Join(errs...)
		st.LastError = joined.Error()
		return st, joined
	}

	own, ownErr := p.reconcileOwnAccess(ctx, hosts)
	if ownErr != nil {
		errs = append(errs, ownErr)
	}
	routes := ingressRoutes(tcfg.Ingress)
	active, blocked := splitBlocked(hosts, own, routes)
	wanted := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		wanted[h.Host] = true
	}

	protected := private
	if p.ownership != nil {
		protected, err = p.ownership.protect(ctx, tcfg.Ingress, private)
		if err != nil {
			st.LastError = err.Error()
			return st, err
		}
	}
	outcome := p.applyIngress(ctx, tcfg, active, protected, routes, wanted)
	errs = append(errs, outcome.errs...)

	entries := p.buildEntries(active, own, routes, private, outcome)
	errs = append(errs, p.reconcileEntries(ctx, entries, outcome.plan, own)...)
	p.collectStatus(&st, entries, blocked, routes, private, wanted)

	if len(errs) == 0 {
		return st, nil
	}
	joined := errors.Join(errs...)
	st.LastError = joined.Error()
	return st, joined
}

func (p *Publisher) observeTunnel(ctx context.Context, connector ConnectorStatus, errs *[]error) *TunnelInfo {
	tunnel, err := p.client.GetTunnel(ctx, p.cfg.AccountID, p.cfg.TunnelID)
	if err != nil {
		*errs = append(*errs, err)
		return &TunnelInfo{ID: p.cfg.TunnelID, Name: p.cfg.Name, Status: connectorTunnelHealth(connector), Connections: connector.ReadyConnections}
	}
	return &TunnelInfo{ID: tunnel.ID, Name: tunnel.Name, Status: tunnel.Status, Connections: len(tunnel.Connections)}
}

func splitBlocked(hosts []Hostname, own map[string]*ownAccess, routes map[string]string) ([]Hostname, []HostStatus) {
	active := make([]Hostname, 0, len(hosts))
	var blocked []HostStatus
	for _, h := range hosts {
		if result := own[h.Host]; result != nil && !result.exists {
			if _, routed := routes[h.Host]; !routed {
				blocked = append(blocked, HostStatus{Host: h.Host, EndpointID: h.EndpointID, DNS: DNSUnchecked, Access: AccessError, AccessApp: AccessAppOwn, AccessPolicy: result.status,
					State: StateError, Detail: result.detail, AccessHostname: h.AccessHostname, AccessHostnameKnown: h.AccessHostnameKnown})
				continue
			}
		}
		active = append(active, h)
	}
	return active, blocked
}

func (p *Publisher) applyIngress(ctx context.Context, tcfg *TunnelConfig, active []Hostname, protected map[string]string, routes map[string]string, wanted map[string]bool) ingressOutcome {
	names := make([]string, 0, len(active))
	for _, h := range active {
		names = append(names, h.Host)
	}
	plan := planIngress(tcfg.Ingress, p.cfg.Origin, names, protected, p.cfg.Prune)
	if p.ownership != nil {
		p.ownership.includeResiduals(&plan, routes, wanted, protected)
	}
	outcome := ingressOutcome{plan: plan, observedRules: tcfg.Ingress}
	if !plan.changed {
		return outcome
	}
	tcfg.Ingress = plan.rules
	outcome.putErr = p.client.PutTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID, tcfg)
	if outcome.putErr != nil {
		outcome.errs = append(outcome.errs, outcome.putErr)
		return outcome
	}
	if p.ownership != nil {
		observed, readErr := p.client.GetTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID)
		if readErr != nil {
			outcome.errs = append(outcome.errs, readErr)
			outcome.observedRules = nil
		} else {
			outcome.observedRules = observed.Ingress
		}
	}
	return outcome
}

func (p *Publisher) buildEntries(active []Hostname, own map[string]*ownAccess, routes, private map[string]string, outcome ingressOutcome) []*hostEntry {
	entries := make([]*hostEntry, 0, len(active)+len(outcome.plan.orphans))
	for _, h := range active {
		entries = append(entries, p.activeEntry(h, own[h.Host], routes, outcome))
	}
	for _, h := range outcome.plan.orphans {
		entries = append(entries, orphanEntry(h, routes, private, outcome))
	}
	return entries
}

func (p *Publisher) activeEntry(h Hostname, own *ownAccess, routes map[string]string, outcome ingressOutcome) *hostEntry {
	plan := outcome.plan
	e := &hostEntry{own: own, HostStatus: HostStatus{Host: h.Host, EndpointID: h.EndpointID, DNS: DNSMissing, Access: AccessNA, Service: routes[h.Host], AccessHostname: h.AccessHostname, AccessHostnameKnown: h.AccessHostnameKnown}}
	switch {
	case plan.present[h.Host]:
		e.Ingress = true
	case plan.added[h.Host]:
		if outcome.putErr != nil {
			e.failed = true
			e.Detail = outcome.putErr.Error()
		} else {
			e.Ingress = true
			e.Service = p.cfg.Origin
		}
	default:
		e.conflict = true
		e.Ingress = true
		e.Detail = plan.conflicts[h.Host]
		e.Fix = fmt.Sprintf("Remove the public hostname %s from tunnel %s or point it at %s; mishmesh does not overwrite routes it did not create.", h.Host, p.tunnelLabel(), p.cfg.Origin)
	}
	if p.ownership != nil && e.Ingress && !e.conflict && !e.failed {
		p.ownership.recordIngress(h.Host, outcome.observedRules, plan.added[h.Host] && outcome.putErr == nil)
	}
	p.describeAccess(e)
	return e
}

func orphanEntry(host string, routes, private map[string]string, outcome ingressOutcome) *hostEntry {
	e := &hostEntry{orphan: true, HostStatus: HostStatus{Host: host, Ingress: true, Service: routes[host], DNS: DNSMissing, Access: AccessNA}}
	e.Detail = unclaimedOwned
	if endpointID, known := private[host]; known {
		e.EndpointID, e.Detail, e.Fix = endpointID, unclaimedOff, unclaimedFix
	}
	if outcome.plan.removed[host] {
		if outcome.putErr != nil {
			e.failed = true
			e.Detail = outcome.putErr.Error()
		} else {
			e.Ingress = false
			e.Detail = "removed by prune"
		}
	}
	return e
}

func (p *Publisher) reconcileEntries(ctx context.Context, entries []*hostEntry, plan ingressPlan, own map[string]*ownAccess) []error {
	var errs []error
	zones := &zoneResolver{client: p.client, accountID: p.cfg.AccountID}
	lookups := p.lookupDNS(ctx, zones, entries)
	for i, e := range entries {
		p.reconcileDNS(ctx, lookups[i], e, plan)
	}
	if p.cfg.AccessAppID != "" {
		if err := p.reconcileAccess(ctx, entries); err != nil {
			errs = append(errs, err)
		}
	}
	if err := p.pruneOwnAccess(ctx, entries, own); err != nil {
		errs = append(errs, err)
	}
	return errs
}

func (p *Publisher) collectStatus(st *Status, entries []*hostEntry, blocked []HostStatus, routes, private map[string]string, wanted map[string]bool) {
	for _, e := range entries {
		e.State = entryState(e)
		st.Hostnames = append(st.Hostnames, e.HostStatus)
		wanted[e.Host] = true
		if p.ownership != nil && e.orphan && !e.Ingress && !e.failed {
			p.ownership.update(e.Host, func(owned *ownedRoute) { owned.Ingress = "" })
		}
		if e.State == StateError {
			p.logHostError(e)
		}
	}
	st.Hostnames = append(st.Hostnames, blocked...)
	st.Hostnames = append(st.Hostnames, unclaimedRoutes(routes, wanted, private, normalizeService(p.cfg.Origin), DNSUnchecked, AccessNA)...)
}

func (p *Publisher) logHostError(e *hostEntry) {
	if p.cfg.ConnectionID != "" {
		p.log.Warn("cloudflare hostname error", "host", e.Host, "dns", e.DNS, "access", e.Access)
		return
	}
	p.log.Warn("cloudflare hostname error", "host", e.Host, "dns", e.DNS, "dns_detail", e.DNSDetail, "access", e.Access, "detail", e.Detail)
}

func ingressRoutes(rules []IngressRule) map[string]string {
	routes := make(map[string]string, len(rules))
	for _, r := range rules {
		h := normalizeHost(r.Hostname())
		if h == "" || r.Path() != "" {
			continue
		}
		if _, seen := routes[h]; !seen {
			routes[h] = r.Service()
		}
	}
	return routes
}
