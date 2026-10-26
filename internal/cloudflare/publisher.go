package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"
)

type Publisher struct {
	cfg            *Config
	src            Source
	log            *slog.Logger
	client         *Client
	local          *LocalReader
	supervisor     *Supervisor
	ownership      *routeOwnership
	locks          *providerLocks
	debounce       time.Duration
	trigger        chan struct{}
	publishAliases func(context.Context, *Status) error

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
	if p.locks != nil {
		release, err := p.locks.acquire(ctx, p.cfg)
		if err != nil {
			return p.Status(), errors.New("cloudflare sync canceled")
		}
		defer release()
	}
	var st Status
	var err error
	var previousOwnership ownershipState
	if p.ownership != nil {
		previousOwnership = p.ownership.snapshot()
	}
	if p.client != nil {
		st, err = p.reconcile(ctx)
	} else {
		st, err = p.inspect(ctx)
	}
	if p.ownership != nil {
		if stateErr := p.ownership.save(ctx); stateErr != nil {
			p.ownership.state = previousOwnership
			err = errors.Join(err, stateErr)
			st.LastError = stateErr.Error()
		}
	}
	if p.cfg.ConnectionID != "" {
		if err != nil {
			err = errors.New("cloudflare sync failed")
		}
		if st.LastError != "" {
			st.LastError = "cloudflare sync failed"
		}
		if st.Connector.Error != "" {
			st.Connector.Error = "cloudflared observation failed"
		}
		for index := range st.Hostnames {
			if st.Hostnames[index].State == StateError {
				st.Hostnames[index].Detail = "cloudflare hostname reconciliation failed"
			}
		}
	}
	if p.publishAliases != nil {
		if aliasErr := p.publishAliases(ctx, &st); aliasErr != nil {
			err, st.LastError = errors.Join(err, aliasErr), "access aliases could not be saved"
		}
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
		hs := HostStatus{Host: h.Host, EndpointID: h.EndpointID, DNS: DNSUnchecked, Access: p.accessUnchecked(), AccessHostname: h.AccessHostname, AccessHostnameKnown: h.AccessHostnameKnown}
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
