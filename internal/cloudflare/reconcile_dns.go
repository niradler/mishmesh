package cloudflare

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type zoneResolver struct {
	client    *Client
	accountID string
	zones     []Zone
	loaded    bool
	err       error
}

func (z *zoneResolver) find(ctx context.Context, host string) (Zone, error) {
	if !z.loaded {
		z.zones, z.err = z.client.ListZones(ctx, z.accountID)
		z.loaded = true
	}
	if z.err != nil {
		return Zone{}, z.err
	}
	var best Zone
	for _, zone := range z.zones {
		name := normalizeHost(zone.Name)
		if host == name || strings.HasSuffix(host, "."+name) {
			if len(name) > len(best.Name) {
				best = Zone{ID: zone.ID, Name: name}
			}
		}
	}
	if best.ID == "" {
		return Zone{}, fmt.Errorf("%w %s", errNoZone, host)
	}
	return best, nil
}

func (p *Publisher) tunnelTarget() string {
	return p.cfg.TunnelID + ".cfargotunnel.com"
}

func (p *Publisher) pointsAtTunnel(r DNSRecord) bool {
	return strings.EqualFold(r.Type, "CNAME") && normalizeHost(r.Content) == strings.ToLower(p.tunnelTarget())
}

const dnsLookupConcurrency = 8

type dnsLookup struct {
	zone    Zone
	records []DNSRecord
	err     error
}

func (p *Publisher) lookupDNS(ctx context.Context, zones *zoneResolver, entries []*hostEntry) []dnsLookup {
	lookups := make([]dnsLookup, len(entries))
	slots := make(chan struct{}, dnsLookupConcurrency)
	var wg sync.WaitGroup
	for i, e := range entries {
		zone, err := zones.find(ctx, e.Host)
		if err != nil {
			lookups[i].err = err
			continue
		}
		lookups[i].zone = zone
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			lookups[i].records, lookups[i].err = p.client.ListDNSRecords(ctx, zone.ID, e.Host)
		})
	}
	wg.Wait()
	return lookups
}

func (p *Publisher) reconcileDNS(ctx context.Context, lookup dnsLookup, e *hostEntry, plan ingressPlan) {
	if lookup.err != nil {
		e.DNS, e.DNSDetail = DNSError, lookup.err.Error()
		return
	}
	zone, recs := lookup.zone, lookup.records
	var ours []DNSRecord
	for _, r := range recs {
		if p.pointsAtTunnel(r) {
			ours = append(ours, r)
		}
	}
	pruneThis := e.orphan && p.cfg.Prune && plan.removed[e.Host] && !e.failed
	switch {
	case pruneThis && len(ours) > 0:
		preserved := false
		for _, r := range ours {
			if p.ownership != nil && !p.ownership.ownsDNS(e.Host, r) {
				preserved = true
				continue
			}
			if err := p.client.DeleteDNSRecord(ctx, zone.ID, r.ID); err != nil {
				e.DNS, e.DNSDetail = DNSError, err.Error()
				return
			}
		}
		if preserved {
			e.DNS, e.DNSDetail = DNSOK, "changed DNS record preserved"
		} else {
			e.DNS, e.DNSDetail = DNSMissing, "deleted by prune"
			if p.ownership != nil {
				p.ownership.update(e.Host, func(owned *ownedRoute) { owned.DNS = nil })
			}
		}
	case len(ours) > 0:
		e.DNS = DNSOK
		if p.ownership != nil && !e.orphan && !e.conflict {
			p.ownership.recordDNS(e.Host, ours, false)
		}
	case len(recs) > 0:
		e.DNS = DNSConflict
		e.DNSDetail = fmt.Sprintf("existing %s record %s is not managed by this tunnel", recs[0].Type, recs[0].Content)
		if e.Fix == "" && !e.orphan {
			e.Fix = fmt.Sprintf("In the Cloudflare DNS settings for %s, replace the %s record for %s with a proxied CNAME to %s.", zone.Name, recs[0].Type, e.Host, p.tunnelTarget())
		}
	case e.orphan || e.conflict:
		e.DNS = DNSMissing
	default:
		rec := DNSRecord{Type: "CNAME", Name: e.Host, Content: p.tunnelTarget(), Proxied: true, Comment: dnsComment}
		created, err := p.client.createDNSRecord(ctx, zone.ID, rec)
		if err != nil {
			e.DNS, e.DNSDetail = DNSError, err.Error()
			return
		}
		e.DNS, e.DNSDetail = DNSOK, "created"
		if p.ownership != nil {
			p.ownership.recordDNS(e.Host, []DNSRecord{created}, true)
		}
	}
}
