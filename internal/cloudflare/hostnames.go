package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/mishmesh/mishmesh/internal/connections"
)

var (
	errNoZone            = errors.New("no zone in the account matches")
	directServiceSchemes = map[string]bool{"http": true, "https": true, "tcp": true, "ssh": true, "rdp": true}
)

func hostnameError(kind connections.HostnameErrorKind, format string, args ...any) *connections.HostnameError {
	return &connections.HostnameError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

func (i *providerInstance) AddHostname(ctx context.Context, host, service string) error {
	return i.publisher.AddHostname(ctx, host, service)
}

func (i *providerInstance) RemoveHostname(ctx context.Context, host string) error {
	return i.publisher.RemoveHostname(ctx, host)
}

func (p *Publisher) lockForChange(ctx context.Context) (func(), error) {
	if p.client == nil {
		return nil, hostnameError(connections.HostnameUnsupported, "add an API token to manage hostnames from mishmesh")
	}
	p.syncMu.Lock()
	if p.locks == nil {
		return p.syncMu.Unlock, nil
	}
	release, err := p.locks.acquire(ctx, p.cfg)
	if err != nil {
		p.syncMu.Unlock()
		return nil, errors.New("cloudflare hostname change canceled")
	}
	return func() { release(); p.syncMu.Unlock() }, nil
}

func (p *Publisher) checkDirectService(service string) (string, error) {
	service = strings.TrimSpace(service)
	parsed, err := url.Parse(service)
	if err != nil || !directServiceSchemes[parsed.Scheme] || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", hostnameError(connections.HostnameInvalid, "service must be an http, https, tcp, ssh or rdp URL with a host and no path, for example http://localhost:8000")
	}
	if normalizeService(service) == normalizeService(p.cfg.Origin) {
		return "", hostnameError(connections.HostnameInvalid, "to route through mishmesh, add the hostname to an endpoint instead")
	}
	return service, nil
}

func (p *Publisher) AddHostname(ctx context.Context, host, service string) error {
	host, err := accessHostname("", host)
	if err != nil {
		return hostnameError(connections.HostnameInvalid, "%s", err.Error())
	}
	if service, err = p.checkDirectService(service); err != nil {
		return err
	}
	release, err := p.lockForChange(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := p.checkUnclaimed(ctx, host); err != nil {
		return err
	}
	tcfg, err := p.client.GetTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID)
	if err != nil {
		return err
	}
	if routed, ok := ingressRoutes(tcfg.Ingress)[host]; ok {
		return hostnameError(connections.HostnameConflict, "%s is already routed to %s", host, routed)
	}
	if _, present := withoutHost(tcfg.Ingress, host); present {
		return hostnameError(connections.HostnameConflict, "%s already has path rules on this tunnel", host)
	}
	zone, err := (&zoneResolver{client: p.client, accountID: p.cfg.AccountID}).find(ctx, host)
	if errors.Is(err, errNoZone) {
		return hostnameError(connections.HostnameInvalid, "no zone this API token can see matches %s", host)
	}
	if err != nil {
		return err
	}
	records, err := p.client.ListDNSRecords(ctx, zone.ID, host)
	if err != nil {
		return err
	}
	hasTunnelRecord := false
	for _, record := range records {
		if !p.pointsAtTunnel(record) {
			return hostnameError(connections.HostnameConflict, "an existing %s record for %s points at %s", record.Type, host, record.Content)
		}
		hasTunnelRecord = true
	}
	tcfg.Ingress = insertBeforeCatchAll(tcfg.Ingress, NewIngressRule(host, service))
	if err := p.client.PutTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID, tcfg); err != nil {
		return err
	}
	if hasTunnelRecord {
		return nil
	}
	rec := DNSRecord{Type: "CNAME", Name: host, Content: p.tunnelTarget(), Proxied: true, Comment: dnsComment}
	if _, err := p.client.createDNSRecord(ctx, zone.ID, rec); err != nil {
		return errors.Join(err, p.removeIngress(ctx, host))
	}
	return nil
}

func insertBeforeCatchAll(rules []IngressRule, rule IngressRule) []IngressRule {
	out := make([]IngressRule, 0, len(rules)+2)
	if n := len(rules); n > 0 && isCatchAll(rules[n-1]) {
		out = append(out, rules[:n-1]...)
		return append(out, rule, rules[n-1])
	}
	out = append(out, rules...)
	return append(out, rule, NewIngressRule("", catchAllService))
}

func (p *Publisher) removeIngress(ctx context.Context, host string) error {
	tcfg, err := p.client.GetTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID)
	if err != nil {
		return err
	}
	kept, removed := withoutHost(tcfg.Ingress, host)
	if !removed {
		return nil
	}
	tcfg.Ingress = kept
	return p.client.PutTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID, tcfg)
}

func withoutHost(rules []IngressRule, host string) ([]IngressRule, bool) {
	kept := make([]IngressRule, 0, len(rules))
	for _, rule := range rules {
		if normalizeHost(rule.Hostname()) != host {
			kept = append(kept, rule)
		}
	}
	return kept, len(kept) != len(rules)
}

func (p *Publisher) RemoveHostname(ctx context.Context, host string) error {
	host, err := accessHostname("", host)
	if err != nil {
		return hostnameError(connections.HostnameInvalid, "%s", err.Error())
	}
	release, err := p.lockForChange(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := p.checkUnclaimed(ctx, host); err != nil {
		return err
	}
	tcfg, err := p.client.GetTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID)
	if err != nil {
		return err
	}
	kept, routed := withoutHost(tcfg.Ingress, host)
	var tunnelRecords []DNSRecord
	zone, err := (&zoneResolver{client: p.client, accountID: p.cfg.AccountID}).find(ctx, host)
	switch {
	case errors.Is(err, errNoZone):
	case err != nil:
		return err
	default:
		records, err := p.client.ListDNSRecords(ctx, zone.ID, host)
		if err != nil {
			return err
		}
		for _, record := range records {
			if p.pointsAtTunnel(record) {
				tunnelRecords = append(tunnelRecords, record)
			}
		}
	}
	if !routed && len(tunnelRecords) == 0 {
		return hostnameError(connections.HostnameNotFound, "%s is not on this tunnel", host)
	}
	if routed {
		tcfg.Ingress = kept
		if err := p.client.PutTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID, tcfg); err != nil {
			return err
		}
	}
	for _, record := range tunnelRecords {
		if err := p.client.DeleteDNSRecord(ctx, zone.ID, record.ID); err != nil {
			return err
		}
	}
	return p.forgetOwnership(ctx, host)
}

func (p *Publisher) checkUnclaimed(ctx context.Context, host string) error {
	if err := p.checkUnreferenced(ctx, host); err != nil {
		return err
	}
	if p.ownership == nil {
		return nil
	}
	claims, err := p.ownership.foreignClaims(ctx)
	if err != nil {
		return err
	}
	if _, claimed := claims[host]; claimed {
		return hostnameError(connections.HostnameConflict, "%s is published through another connection", host)
	}
	return nil
}

func (p *Publisher) checkUnreferenced(ctx context.Context, host string) error {
	all, err := p.src.Hostnames(ctx, p.cfg.OrgID)
	if err != nil {
		return fmt.Errorf("list desired hostnames: %w", err)
	}
	var endpointIDs []string
	for _, h := range all {
		if h.Public && normalizeHost(h.Host) == host {
			endpointIDs = append(endpointIDs, h.EndpointID)
		}
	}
	if len(endpointIDs) == 0 {
		return nil
	}
	return &connections.HostnameError{Kind: connections.HostnameInUse, Message: host + " is published by an endpoint; remove its Access row first", EndpointIDs: endpointIDs}
}

func (p *Publisher) forgetOwnership(ctx context.Context, host string) error {
	if p.ownership == nil {
		return nil
	}
	owned, ok := p.ownership.state.Routes[host]
	if !ok {
		return nil
	}
	if owned.Access != "" && p.cfg.AccessAppID != "" {
		app, err := p.client.GetAccessApp(ctx, p.cfg.AccountID, p.cfg.AccessAppID)
		if err != nil {
			return err
		}
		if accessFingerprint(app, host) == owned.Access && accessRemoveHost(app, host) {
			if err := p.client.PutAccessApp(ctx, p.cfg.AccountID, p.cfg.AccessAppID, app); err != nil {
				return err
			}
		}
	}
	previous := p.ownership.snapshot()
	delete(p.ownership.state.Routes, host)
	if err := p.ownership.save(ctx); err != nil {
		p.ownership.state = previous
		return err
	}
	return nil
}
