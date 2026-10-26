package health

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	StateUp       = "up"
	StateDown     = "down"
	StateDegraded = "degraded"
	StateSleeping = "sleeping"
	StateWaking   = "waking"
	StateOffline  = "offline"
	StateUnknown  = "unknown"
)

const (
	ServiceRunning  = services.StateRunning
	ServiceStopped  = services.StateStopped
	ServiceSleeping = services.StateSleeping
	ServiceStarting = services.StateStarting
	ServicePartial  = services.StatePartial
	ServiceUnknown  = services.StateUnknown
	ServiceError    = services.StateError
)

const (
	defaultInterval = 15 * time.Second
	defaultTimeout  = 3 * time.Second
	maxConcurrent   = 16
)

type TargetStatus struct {
	Target    string    `json:"target"`
	Healthy   bool      `json:"healthy"`
	LatencyMs int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

type EndpointHealth struct {
	State     string         `json:"state"`
	Detail    string         `json:"detail,omitempty"`
	CheckedAt time.Time      `json:"checked_at"`
	LatencyMs int64          `json:"latency_ms,omitempty"`
	Targets   []TargetStatus `json:"targets,omitempty"`
	Service   string         `json:"service,omitempty"`
}

type ServiceStates interface {
	ServiceState(id string) (name, state string, ok bool)
}

type Options struct {
	Data     store.DataStore
	Conns    store.ConnectionStore
	Guard    *proxy.Guard
	Services ServiceStates
	Interval time.Duration
	Timeout  time.Duration
	Log      *slog.Logger
}

type Prober struct {
	data     store.DataStore
	conns    store.ConnectionStore
	guard    *proxy.Guard
	services ServiceStates
	interval time.Duration
	timeout  time.Duration
	log      *slog.Logger

	kick    chan struct{}
	mu      sync.RWMutex
	results map[string]EndpointHealth
}

var _ proxy.TargetHealth = (*Prober)(nil)

func NewProber(o Options) *Prober {
	p := &Prober{
		data: o.Data, conns: o.Conns, guard: o.Guard, services: o.Services,
		interval: o.Interval, timeout: o.Timeout, log: o.Log,
		results: make(map[string]EndpointHealth),
		kick:    make(chan struct{}, 1),
	}
	if p.guard == nil {
		p.guard = proxy.DefaultGuard()
	}
	if p.interval <= 0 {
		p.interval = defaultInterval
	}
	if p.timeout <= 0 {
		p.timeout = defaultTimeout
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	return p
}

func (p *Prober) Run(ctx context.Context) {
	p.sweep(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sweep(ctx)
		case <-p.kick:
			p.sweep(ctx)
		}
	}
}

func (p *Prober) Trigger() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *Prober) sweep(ctx context.Context) {
	eps, err := p.data.ListAllEndpoints(ctx)
	if err != nil {
		p.log.Warn("health: list endpoints", "error", err)
		return
	}
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	seen := make(map[string]bool, len(eps))
	for _, ep := range eps {
		seen[ep.ID] = true
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			p.store(ep.ID, p.probe(ctx, ep))
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	for id := range p.results {
		if !seen[id] {
			delete(p.results, id)
		}
	}
	p.mu.Unlock()
}

func (p *Prober) store(endpointID string, h EndpointHealth) {
	p.mu.Lock()
	p.results[endpointID] = h
	p.mu.Unlock()
}

func (p *Prober) ProbeNow(ctx context.Context, endpointID string) (EndpointHealth, error) {
	ep, err := p.data.GetEndpoint(ctx, endpointID)
	if err != nil {
		return EndpointHealth{}, fmt.Errorf("health: probe endpoint %s: %w", endpointID, err)
	}
	h := p.probe(ctx, ep)
	p.store(endpointID, h)
	return h, nil
}

func (p *Prober) Get(endpointID string) (EndpointHealth, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	h, ok := p.results[endpointID]
	return h, ok
}

func (p *Prober) Forget(endpointID string) {
	p.mu.Lock()
	delete(p.results, endpointID)
	p.mu.Unlock()
}

func (p *Prober) TargetHealthy(endpointID, target string) (bool, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, t := range p.results[endpointID].Targets {
		if t.Target == target {
			return t.Healthy, true
		}
	}
	return false, false
}

func (p *Prober) probe(ctx context.Context, ep *store.Endpoint) EndpointHealth {
	var service string
	if ep.Policy != nil {
		service = ep.Policy.ServiceID
	}
	var h EndpointHealth
	if ep.Method == store.MethodProxy {
		h = p.probeProxy(ctx, ep)
	} else {
		h = p.probeBound(ep)
	}
	h.Service = service
	h.CheckedAt = time.Now()
	return h
}

func (p *Prober) probeBound(ep *store.Endpoint) EndpointHealth {
	if p.conns == nil {
		return EndpointHealth{State: StateUnknown, Detail: "no connection store"}
	}
	if _, ok := p.conns.ResolveEndpoint(ep.ID); !ok {
		return EndpointHealth{State: StateOffline, Detail: "no agent bound"}
	}
	return EndpointHealth{State: StateUp}
}

func (p *Prober) probeProxy(ctx context.Context, ep *store.Endpoint) EndpointHealth {
	if ep.Policy == nil || len(ep.Policy.ProxyTargets) == 0 {
		return EndpointHealth{State: StateDown, Detail: "no proxy_targets configured"}
	}
	targets := ep.Policy.ProxyTargets
	statuses := make([]TargetStatus, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = p.probeTarget(ctx, target, ep.Policy.OriginTLS)
		}()
	}
	wg.Wait()
	h := summarize(statuses)
	if p.services != nil && ep.Policy.ServiceID != "" {
		applyServiceState(&h, ep.Policy.ServiceID, p.services)
	}
	return h
}

func applyServiceState(h *EndpointHealth, id string, services ServiceStates) {
	name, state, ok := services.ServiceState(id)
	if !ok {
		h.State = StateDown
		h.Detail = fmt.Sprintf("service %s is not loaded on this server", id)
		return
	}
	switch state {
	case ServiceStarting:
		h.State = StateWaking
		h.Detail = fmt.Sprintf("service %s is starting", name)
	case ServiceSleeping:
		if h.State != StateUp {
			h.State = StateSleeping
			h.Detail = fmt.Sprintf("service %s is scaled to zero and wakes on request", name)
		}
	case ServiceStopped:
		if h.State != StateUp {
			h.State = StateSleeping
			h.Detail = fmt.Sprintf("service %s was stopped outside mishmesh and wakes on request", name)
		}
	case ServiceError:
		if h.State != StateUp {
			h.State = StateDown
			h.Detail = fmt.Sprintf("service %s failed; a request will try to start it again", name)
		}
	}
}

func (p *Prober) probeTarget(ctx context.Context, target string, otls *store.OriginTLS) TargetStatus {
	st := TargetStatus{Target: target, CheckedAt: time.Now()}
	start := time.Now()
	c, err := p.guard.Dial(ctx, target, otls, p.timeout)
	st.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	_ = c.Close()
	st.Healthy = true
	return st
}

func summarize(statuses []TargetStatus) EndpointHealth {
	h := EndpointHealth{Targets: statuses}
	up := 0
	var firstErr string
	minLatency := int64(-1)
	for _, s := range statuses {
		if s.Healthy {
			up++
			if minLatency < 0 || s.LatencyMs < minLatency {
				minLatency = s.LatencyMs
			}
		} else if firstErr == "" {
			firstErr = s.Error
		}
	}
	switch {
	case up == len(statuses):
		h.State = StateUp
	case up == 0:
		h.State = StateDown
		h.Detail = firstErr
	default:
		h.State = StateDegraded
		h.Detail = fmt.Sprintf("%d of %d targets down: %s", len(statuses)-up, len(statuses), firstErr)
	}
	if minLatency > 0 {
		h.LatencyMs = minLatency
	}
	return h
}
