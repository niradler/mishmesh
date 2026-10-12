package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
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
	Host       string `json:"host"`
	EndpointID string `json:"endpoint_id,omitempty"`
	Ingress    bool   `json:"ingress"`
	Service    string `json:"service,omitempty"`
	DNS        string `json:"dns"`
	DNSDetail  string `json:"dns_detail,omitempty"`
	Access     string `json:"access"`
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
	Fix        string `json:"fix,omitempty"`
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

type Publisher struct {
	cfg        *Config
	src        Source
	log        *slog.Logger
	client     *Client
	local      *LocalReader
	supervisor *Supervisor
	debounce   time.Duration
	trigger    chan struct{}

	syncMu sync.Mutex
	mu     sync.RWMutex
	status Status
}

func NewPublisher(cfg *Config, src Source, log *slog.Logger) (*Publisher, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = DefaultMetricsAddr
	}
	local, err := NewLocalReader(cfg.MetricsAddr)
	if err != nil {
		return nil, err
	}
	p := &Publisher{
		cfg:      cfg,
		src:      src,
		log:      log.With("org", cfg.OrgID, "tunnel", cfg.Name),
		local:    local,
		debounce: defaultDebounce,
		trigger:  make(chan struct{}, 1),
		status:   Status{Level: cfg.Level(), Origin: cfg.Origin, Connector: ConnectorStatus{EdgeLocations: []string{}}, Hostnames: []HostStatus{}},
	}
	if cfg.Level() == LevelManaged {
		p.client = NewClient(cfg.BaseURL, cfg.APIToken)
	}
	return p, nil
}

func (p *Publisher) Config() Config {
	return *p.cfg
}

func (p *Publisher) Trigger() {
	select {
	case p.trigger <- struct{}{}:
	default:
	}
}

func (p *Publisher) Status() Status {
	p.mu.RLock()
	st := p.status
	st.Hostnames = slices.Clone(p.status.Hostnames)
	st.Connector.EdgeLocations = slices.Clone(p.status.Connector.EdgeLocations)
	p.mu.RUnlock()
	if p.supervisor != nil {
		sup := p.supervisor.Status()
		st.Supervisor = &sup
	}
	return st
}

func (p *Publisher) Run(ctx context.Context) {
	p.syncLogged(ctx)
	ticker := time.NewTicker(p.cfg.refreshInterval())
	defer ticker.Stop()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var debounced <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.syncLogged(ctx)
		case <-p.trigger:
			timer.Reset(p.debounce)
			debounced = timer.C
		case <-debounced:
			debounced = nil
			p.syncLogged(ctx)
		}
	}
}

func (p *Publisher) syncLogged(ctx context.Context) {
	if _, err := p.Sync(ctx); err != nil && ctx.Err() == nil {
		p.log.Error("cloudflare sync failed", "error", err)
	}
}

func (p *Publisher) Sync(ctx context.Context) (Status, error) {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()
	var st Status
	var err error
	if p.client != nil {
		st, err = p.reconcile(ctx)
	} else {
		st, err = p.inspect(ctx)
	}
	p.mu.Lock()
	p.status = st
	p.mu.Unlock()
	return p.Status(), err
}

func (p *Publisher) baseStatus(ctx context.Context) Status {
	return Status{
		Level:     p.cfg.Level(),
		Origin:    p.cfg.Origin,
		LastSync:  time.Now().UTC(),
		Connector: p.local.Status(ctx),
		Hostnames: []HostStatus{},
	}
}

func (p *Publisher) desired(ctx context.Context, st *Status) ([]Hostname, map[string]string, error) {
	all, err := p.src.Hostnames(ctx, p.cfg.OrgID)
	if err != nil {
		err = fmt.Errorf("list desired hostnames: %w", err)
		st.LastError = err.Error()
		st.Hostnames = p.Status().Hostnames
		return nil, nil, err
	}
	var public []Hostname
	private := make(map[string]string)
	for _, h := range all {
		if h.Public {
			public = append(public, h)
		} else if _, seen := private[h.Host]; !seen {
			private[h.Host] = h.EndpointID
		}
	}
	return dedupeHostnames(public), private, nil
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

func (p *Publisher) accessUnchecked() string {
	if p.cfg.AccessAppID == "" {
		return AccessNA
	}
	return AccessUnchecked
}

func (p *Publisher) inspect(ctx context.Context) (Status, error) {
	st := p.baseStatus(ctx)
	st.Tunnel = &TunnelInfo{
		ID:          p.cfg.TunnelID,
		Name:        p.cfg.Name,
		Status:      connectorTunnelHealth(st.Connector),
		Connections: st.Connector.ReadyConnections,
	}
	hosts, private, err := p.desired(ctx, &st)
	if err != nil {
		return st, err
	}
	var rules []IngressRule
	if st.Connector.Reachable {
		rules, err = p.local.Ingress(ctx)
	} else {
		err = errors.New(st.Connector.Error)
	}
	if err != nil {
		err = fmt.Errorf("read tunnel hostnames from cloudflared: %w", err)
		for _, h := range hosts {
			st.Hostnames = append(st.Hostnames, HostStatus{
				Host: h.Host, EndpointID: h.EndpointID, DNS: DNSUnchecked, Access: p.accessUnchecked(),
				State: StateError, Detail: err.Error(),
			})
		}
		st.LastError = err.Error()
		return st, err
	}

	routes := ingressRoutes(rules)
	originKey := normalizeService(p.cfg.Origin)
	wanted := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		wanted[h.Host] = true
		hs := HostStatus{Host: h.Host, EndpointID: h.EndpointID, DNS: DNSUnchecked, Access: p.accessUnchecked()}
		service, routed := routes[h.Host]
		switch {
		case !routed:
			hs.State = StatePending
			hs.Detail = "the tunnel has no public hostname for this domain yet"
			hs.Fix = fmt.Sprintf("In Cloudflare Zero Trust, open Networks > Tunnels > %s > Public hostnames, add %s and set its service to %s.", p.tunnelLabel(), h.Host, p.cfg.Origin)
		case normalizeService(service) != originKey:
			hs.Ingress, hs.Service = true, service
			hs.State = StateConflict
			hs.Detail = "the tunnel routes this hostname to " + service + ", not to mishmesh"
			hs.Fix = fmt.Sprintf("In Cloudflare Zero Trust, open Networks > Tunnels > %s > Public hostnames, edit %s and set its service to %s.", p.tunnelLabel(), h.Host, p.cfg.Origin)
		default:
			hs.Ingress, hs.Service = true, service
			hs.State = StatePublished
		}
		st.Hostnames = append(st.Hostnames, hs)
	}
	st.Hostnames = append(st.Hostnames, unclaimedRoutes(routes, wanted, private, originKey, DNSUnchecked, AccessNA)...)
	return st, nil
}

func (p *Publisher) tunnelLabel() string {
	if p.cfg.Name != "" {
		return p.cfg.Name
	}
	return p.cfg.TunnelID
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

func unclaimedRoutes(routes map[string]string, wanted map[string]bool, private map[string]string, originKey, dns, access string) []HostStatus {
	var out []HostStatus
	for h, service := range routes {
		if wanted[h] {
			continue
		}
		hs := HostStatus{Host: h, Ingress: true, Service: service, DNS: dns, Access: access, State: StateUnclaimed, Detail: unclaimedOwned}
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
	orphan   bool
	conflict bool
	failed   bool
}

func (p *Publisher) reconcile(ctx context.Context) (Status, error) {
	st := p.baseStatus(ctx)
	hosts, private, err := p.desired(ctx, &st)
	if err != nil {
		return st, err
	}

	var errs []error
	tunnel, terr := p.client.GetTunnel(ctx, p.cfg.AccountID, p.cfg.TunnelID)
	if terr != nil {
		errs = append(errs, terr)
		st.Tunnel = &TunnelInfo{ID: p.cfg.TunnelID, Name: p.cfg.Name, Status: connectorTunnelHealth(st.Connector), Connections: st.Connector.ReadyConnections}
	} else {
		st.Tunnel = &TunnelInfo{ID: tunnel.ID, Name: tunnel.Name, Status: tunnel.Status, Connections: len(tunnel.Connections)}
	}

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

	names := make([]string, len(hosts))
	wanted := make(map[string]bool, len(hosts))
	for i, h := range hosts {
		names[i] = h.Host
		wanted[h.Host] = true
	}
	routes := ingressRoutes(tcfg.Ingress)
	plan := planIngress(tcfg.Ingress, p.cfg.Origin, names, private, p.cfg.Prune)
	var putErr error
	if plan.changed {
		tcfg.Ingress = plan.rules
		putErr = p.client.PutTunnelConfig(ctx, p.cfg.AccountID, p.cfg.TunnelID, tcfg)
		if putErr != nil {
			errs = append(errs, putErr)
		}
	}

	entries := make([]*hostEntry, 0, len(hosts)+len(plan.orphans))
	for _, h := range hosts {
		e := &hostEntry{HostStatus: HostStatus{Host: h.Host, EndpointID: h.EndpointID, DNS: DNSMissing, Access: AccessNA, Service: routes[h.Host]}}
		switch {
		case plan.present[h.Host]:
			e.Ingress = true
		case plan.added[h.Host]:
			if putErr != nil {
				e.failed = true
				e.Detail = putErr.Error()
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
		entries = append(entries, e)
	}
	for _, h := range plan.orphans {
		e := &hostEntry{orphan: true, HostStatus: HostStatus{Host: h, Ingress: true, Service: routes[h], DNS: DNSMissing, Access: AccessNA}}
		e.Detail = unclaimedOwned
		if endpointID, known := private[h]; known {
			e.EndpointID, e.Detail, e.Fix = endpointID, unclaimedOff, unclaimedFix
		}
		if plan.removed[h] {
			if putErr != nil {
				e.failed = true
				e.Detail = putErr.Error()
			} else {
				e.Ingress = false
				e.Detail = "removed by prune"
			}
		}
		entries = append(entries, e)
	}

	zones := &zoneResolver{client: p.client, accountID: p.cfg.AccountID}
	for _, e := range entries {
		p.reconcileDNS(ctx, zones, e, plan)
	}
	if p.cfg.AccessAppID != "" {
		if err := p.reconcileAccess(ctx, entries); err != nil {
			errs = append(errs, err)
		}
	}

	for _, e := range entries {
		e.State = entryState(e)
		st.Hostnames = append(st.Hostnames, e.HostStatus)
		wanted[e.Host] = true
		if e.State == StateError {
			p.log.Warn("cloudflare hostname error", "host", e.Host, "dns", e.DNS, "dns_detail", e.DNSDetail, "access", e.Access, "detail", e.Detail)
		}
	}
	st.Hostnames = append(st.Hostnames, unclaimedRoutes(routes, wanted, private, normalizeService(p.cfg.Origin), DNSUnchecked, AccessNA)...)
	if len(errs) == 0 {
		return st, nil
	}
	joined := errors.Join(errs...)
	st.LastError = joined.Error()
	return st, joined
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

func dedupeHostnames(in []Hostname) []Hostname {
	seen := make(map[string]bool, len(in))
	out := make([]Hostname, 0, len(in))
	for _, h := range in {
		h.Host = normalizeHost(h.Host)
		if h.Host == "" || seen[h.Host] {
			continue
		}
		seen[h.Host] = true
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

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
		return Zone{}, fmt.Errorf("no zone in the account matches %s", host)
	}
	return best, nil
}

func (p *Publisher) tunnelTarget() string {
	return p.cfg.TunnelID + ".cfargotunnel.com"
}

func (p *Publisher) pointsAtTunnel(r DNSRecord) bool {
	return strings.EqualFold(r.Type, "CNAME") && normalizeHost(r.Content) == strings.ToLower(p.tunnelTarget())
}

func (p *Publisher) reconcileDNS(ctx context.Context, zones *zoneResolver, e *hostEntry, plan ingressPlan) {
	zone, err := zones.find(ctx, e.Host)
	if err != nil {
		e.DNS, e.DNSDetail = DNSError, err.Error()
		return
	}
	recs, err := p.client.ListDNSRecords(ctx, zone.ID, e.Host)
	if err != nil {
		e.DNS, e.DNSDetail = DNSError, err.Error()
		return
	}
	var ours []DNSRecord
	for _, r := range recs {
		if p.pointsAtTunnel(r) {
			ours = append(ours, r)
		}
	}
	pruneThis := e.orphan && p.cfg.Prune && plan.removed[e.Host] && !e.failed
	switch {
	case pruneThis && len(ours) > 0:
		for _, r := range ours {
			if err := p.client.DeleteDNSRecord(ctx, zone.ID, r.ID); err != nil {
				e.DNS, e.DNSDetail = DNSError, err.Error()
				return
			}
		}
		e.DNS, e.DNSDetail = DNSMissing, "deleted by prune"
	case len(ours) > 0:
		e.DNS = DNSOK
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
		if err := p.client.CreateDNSRecord(ctx, zone.ID, rec); err != nil {
			e.DNS, e.DNSDetail = DNSError, err.Error()
			return
		}
		e.DNS, e.DNSDetail = DNSOK, "created"
	}
}

func (p *Publisher) reconcileAccess(ctx context.Context, entries []*hostEntry) error {
	app, err := p.client.GetAccessApp(ctx, p.cfg.AccountID, p.cfg.AccessAppID)
	if err != nil {
		for _, e := range entries {
			e.Access = AccessError
			if e.Detail == "" {
				e.Detail = err.Error()
			}
		}
		return err
	}
	var touched []*hostEntry
	for _, e := range entries {
		switch {
		case e.orphan && p.cfg.Prune && !e.Ingress && !e.failed:
			if accessRemoveHost(app, e.Host) {
				touched = append(touched, e)
			}
			e.Access = AccessMissing
		case e.orphan:
			e.Access = accessLabel(accessHasHost(app, e.Host))
		case e.conflict:
			e.Access = accessLabel(accessHasHost(app, e.Host))
		default:
			if accessAddHost(app, e.Host) {
				touched = append(touched, e)
			}
			e.Access = AccessOK
		}
	}
	if len(touched) == 0 {
		return nil
	}
	if err := p.client.PutAccessApp(ctx, p.cfg.AccountID, p.cfg.AccessAppID, app); err != nil {
		for _, e := range touched {
			e.Access = AccessError
		}
		return err
	}
	return nil
}

func accessLabel(present bool) string {
	if present {
		return AccessOK
	}
	return AccessMissing
}
