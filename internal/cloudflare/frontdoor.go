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

	DNSOK       = "ok"
	DNSMissing  = "missing"
	DNSConflict = "conflict"
	DNSError    = "error"

	AccessOK      = "ok"
	AccessMissing = "missing"
	AccessNA      = "n/a"
	AccessError   = "error"

	StateSynced   = "synced"
	StatePending  = "pending"
	StateConflict = "conflict"
	StateOrphaned = "orphaned"
	StateError    = "error"
)

type HostStatus struct {
	Host       string `json:"host"`
	EndpointID string `json:"endpoint_id,omitempty"`
	Ingress    bool   `json:"ingress"`
	DNS        string `json:"dns"`
	DNSDetail  string `json:"dns_detail,omitempty"`
	Access     string `json:"access"`
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
}

type Status struct {
	Enabled    bool              `json:"enabled"`
	Tunnel     *Tunnel           `json:"tunnel,omitempty"`
	Origin     string            `json:"origin,omitempty"`
	LastSync   time.Time         `json:"last_sync,omitzero"`
	LastError  string            `json:"last_error,omitempty"`
	Hostnames  []HostStatus      `json:"hostnames"`
	Supervisor *SupervisorStatus `json:"supervisor,omitempty"`
}

type FrontDoor struct {
	cfg        *Config
	src        Source
	log        *slog.Logger
	client     *Client
	supervisor *Supervisor
	orgs       map[string]struct{}
	debounce   time.Duration
	trigger    chan struct{}

	syncMu sync.Mutex
	mu     sync.RWMutex
	status Status
}

func New(cfg *Config, src Source, log *slog.Logger) *FrontDoor {
	if log == nil {
		log = slog.Default()
	}
	f := &FrontDoor{
		cfg:      cfg,
		src:      src,
		log:      log,
		client:   NewClient(cfg.BaseURL, cfg.APIToken),
		orgs:     make(map[string]struct{}, len(cfg.Orgs)),
		debounce: defaultDebounce,
		trigger:  make(chan struct{}, 1),
		status:   Status{Enabled: true, Origin: cfg.Origin, Hostnames: []HostStatus{}},
	}
	for _, o := range cfg.Orgs {
		f.orgs[o] = struct{}{}
	}
	if cfg.Cloudflared != nil {
		f.supervisor = NewSupervisor(*cfg.Cloudflared, log)
	}
	return f
}

func (f *FrontDoor) Enabled(orgID string) bool {
	if f == nil {
		return false
	}
	_, ok := f.orgs[orgID]
	return ok
}

func (f *FrontDoor) Trigger() {
	select {
	case f.trigger <- struct{}{}:
	default:
	}
}

func (f *FrontDoor) Status() Status {
	f.mu.RLock()
	st := f.status
	st.Hostnames = slices.Clone(f.status.Hostnames)
	f.mu.RUnlock()
	if f.supervisor != nil {
		sup := f.supervisor.Status()
		st.Supervisor = &sup
	}
	return st
}

func (f *FrontDoor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if f.supervisor != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.supervisor.Run(ctx)
		}()
	}
	defer wg.Wait()

	f.syncLogged(ctx)
	interval := f.cfg.SyncInterval
	if interval <= 0 {
		interval = DefaultSyncInterval
	}
	ticker := time.NewTicker(interval)
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
			f.syncLogged(ctx)
		case <-f.trigger:
			timer.Reset(f.debounce)
			debounced = timer.C
		case <-debounced:
			debounced = nil
			f.syncLogged(ctx)
		}
	}
}

func (f *FrontDoor) syncLogged(ctx context.Context) {
	if _, err := f.Sync(ctx); err != nil && ctx.Err() == nil {
		f.log.Error("cloudflare sync failed", "error", err)
	}
}

func (f *FrontDoor) Sync(ctx context.Context) (Status, error) {
	f.syncMu.Lock()
	defer f.syncMu.Unlock()
	st, err := f.reconcile(ctx)
	f.mu.Lock()
	f.status = st
	f.mu.Unlock()
	return f.Status(), err
}

type hostEntry struct {
	HostStatus
	orphan   bool
	conflict bool
	failed   bool
}

func (f *FrontDoor) reconcile(ctx context.Context) (Status, error) {
	st := Status{Enabled: true, Origin: f.cfg.Origin, LastSync: time.Now().UTC(), Hostnames: []HostStatus{}}

	desired, err := f.src.DesiredHostnames(ctx)
	if err != nil {
		err = fmt.Errorf("list desired hostnames: %w", err)
		st.LastError = err.Error()
		st.Hostnames = f.Status().Hostnames
		return st, err
	}
	hosts := dedupeHostnames(desired)

	var errs []error
	tunnel, terr := f.client.GetTunnel(ctx, f.cfg.AccountID, f.cfg.TunnelID)
	if terr != nil {
		errs = append(errs, terr)
	} else {
		st.Tunnel = tunnel
	}

	tcfg, err := f.client.GetTunnelConfig(ctx, f.cfg.AccountID, f.cfg.TunnelID)
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
	for i, h := range hosts {
		names[i] = h.Host
	}
	plan := planIngress(tcfg.Ingress, f.cfg.Origin, names, f.cfg.Prune)
	var putErr error
	if plan.changed {
		tcfg.Ingress = plan.rules
		putErr = f.client.PutTunnelConfig(ctx, f.cfg.AccountID, f.cfg.TunnelID, tcfg)
		if putErr != nil {
			errs = append(errs, putErr)
		}
	}

	entries := make([]*hostEntry, 0, len(hosts)+len(plan.orphans))
	for _, h := range hosts {
		e := &hostEntry{HostStatus: HostStatus{Host: h.Host, EndpointID: h.EndpointID, DNS: DNSMissing, Access: AccessNA}}
		switch {
		case plan.present[h.Host]:
			e.Ingress = true
		case plan.added[h.Host]:
			if putErr != nil {
				e.failed = true
				e.Detail = putErr.Error()
			} else {
				e.Ingress = true
			}
		default:
			e.conflict = true
			e.Detail = plan.conflicts[h.Host]
		}
		entries = append(entries, e)
	}
	for _, h := range plan.orphans {
		e := &hostEntry{orphan: true, HostStatus: HostStatus{Host: h, Ingress: true, DNS: DNSMissing, Access: AccessNA}}
		e.Detail = "no endpoint wants this hostname"
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

	zones := &zoneResolver{client: f.client, accountID: f.cfg.AccountID}
	for _, e := range entries {
		f.reconcileDNS(ctx, zones, e, plan)
	}
	if f.cfg.AccessAppID != "" {
		if err := f.reconcileAccess(ctx, entries); err != nil {
			errs = append(errs, err)
		}
	}

	for _, e := range entries {
		e.State = entryState(e)
		st.Hostnames = append(st.Hostnames, e.HostStatus)
		if e.State == StateError {
			f.log.Warn("cloudflare hostname error", "host", e.Host, "dns", e.DNS, "dns_detail", e.DNSDetail, "access", e.Access, "detail", e.Detail)
		}
	}
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
		return StateOrphaned
	}
	switch {
	case e.failed || e.DNS == DNSError || e.Access == AccessError:
		return StateError
	case e.conflict || e.DNS == DNSConflict:
		return StateConflict
	case !e.Ingress || e.DNS == DNSMissing || e.Access == AccessMissing:
		return StatePending
	default:
		return StateSynced
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

func (f *FrontDoor) tunnelTarget() string {
	return f.cfg.TunnelID + ".cfargotunnel.com"
}

func (f *FrontDoor) pointsAtTunnel(r DNSRecord) bool {
	return strings.EqualFold(r.Type, "CNAME") && normalizeHost(r.Content) == strings.ToLower(f.tunnelTarget())
}

func (f *FrontDoor) reconcileDNS(ctx context.Context, zones *zoneResolver, e *hostEntry, plan ingressPlan) {
	zone, err := zones.find(ctx, e.Host)
	if err != nil {
		e.DNS, e.DNSDetail = DNSError, err.Error()
		return
	}
	recs, err := f.client.ListDNSRecords(ctx, zone.ID, e.Host)
	if err != nil {
		e.DNS, e.DNSDetail = DNSError, err.Error()
		return
	}
	var ours []DNSRecord
	for _, r := range recs {
		if f.pointsAtTunnel(r) {
			ours = append(ours, r)
		}
	}
	pruneThis := e.orphan && f.cfg.Prune && plan.removed[e.Host] && !e.failed
	switch {
	case pruneThis && len(ours) > 0:
		for _, r := range ours {
			if err := f.client.DeleteDNSRecord(ctx, zone.ID, r.ID); err != nil {
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
	case e.orphan || e.conflict:
		e.DNS = DNSMissing
	default:
		rec := DNSRecord{Type: "CNAME", Name: e.Host, Content: f.tunnelTarget(), Proxied: true, Comment: dnsComment}
		if err := f.client.CreateDNSRecord(ctx, zone.ID, rec); err != nil {
			e.DNS, e.DNSDetail = DNSError, err.Error()
			return
		}
		e.DNS, e.DNSDetail = DNSOK, "created"
	}
}

func (f *FrontDoor) reconcileAccess(ctx context.Context, entries []*hostEntry) error {
	app, err := f.client.GetAccessApp(ctx, f.cfg.AccountID, f.cfg.AccessAppID)
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
		case e.orphan && f.cfg.Prune && !e.Ingress && !e.failed:
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
	if err := f.client.PutAccessApp(ctx, f.cfg.AccountID, f.cfg.AccessAppID, app); err != nil {
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
