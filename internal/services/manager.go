package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateSleeping = "sleeping"
	StateStarting = "starting"
	StatePartial  = "partial"
	StateUnknown  = "unknown"
	StateError    = "error"

	StartedByMishmesh = "mishmesh"
	StartedByExternal = "external"

	refreshInterval   = 15 * time.Second
	idleCheckInterval = 30 * time.Second
	readyPollInterval = 500 * time.Millisecond
	readyDialTimeout  = 2 * time.Second
	readyHoldWindow   = 300 * time.Millisecond
	driverCallTimeout = 30 * time.Second
	stopTimeout       = 60 * time.Second
)

var ErrUnknownService = errors.New("unknown service")

type Options struct {
	Log      *slog.Logger
	Now      func() time.Time
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	OnChange func()
}

type Status struct {
	Name         string        `json:"name"`
	Driver       string        `json:"driver"`
	State        string        `json:"state"`
	Active       int           `json:"active"`
	LastActivity time.Time     `json:"last_activity,omitzero"`
	IdleTimeout  time.Duration `json:"-"`
	StartTimeout time.Duration `json:"-"`
	LastError    string        `json:"last_error,omitempty"`
	StartedBy    string        `json:"started_by,omitempty"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

func (s Status) MarshalJSON() ([]byte, error) {
	type plain Status
	return json.Marshal(struct {
		plain
		IdleTimeoutSeconds  int `json:"idle_timeout_seconds"`
		StartTimeoutSeconds int `json:"start_timeout_seconds"`
	}{
		plain:               plain(s),
		IdleTimeoutSeconds:  int(s.IdleTimeout / time.Second),
		StartTimeoutSeconds: int(s.StartTimeout / time.Second),
	})
}

type ensureCall struct {
	done chan struct{}
	err  error
}

type service struct {
	name   string
	cfg    ServiceConfig
	driver Driver

	state        string
	active       int
	lastActivity time.Time
	lastError    string
	startedBy    string
	updatedAt    time.Time
	stoppedByUs  bool
	inflight     *ensureCall
}

type Manager struct {
	cfg      *Config
	log      *slog.Logger
	now      func() time.Time
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	onChange func()
	started  time.Time

	pollInterval time.Duration
	readyHold    time.Duration

	mu       sync.Mutex
	services map[string]*service
}

func NewManager(cfg *Config, opts Options) (*Manager, error) {
	drivers := make(map[string]Driver, len(cfg.Services))
	var client *dockerClient
	for name, svc := range cfg.Services {
		switch svc.Driver {
		case DriverDocker, DriverCompose:
			if client == nil {
				var err error
				client, err = newDockerClient(cfg.Docker.Host)
				if err != nil {
					return nil, fmt.Errorf("service %q: %w", name, err)
				}
			}
			if svc.Driver == DriverDocker {
				drivers[name] = newDockerDriver(client, *svc.Docker)
			} else {
				drivers[name] = newComposeDriver(client, *svc.Compose)
			}
		case DriverExec:
			drivers[name] = newExecDriver(*svc.Exec)
		default:
			return nil, fmt.Errorf("service %q: unknown driver %q", name, svc.Driver)
		}
	}
	return newManager(cfg, opts, drivers), nil
}

func newManager(cfg *Config, opts Options, drivers map[string]Driver) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	dial := opts.Dial
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	m := &Manager{
		cfg:          cfg,
		log:          log.With("component", "services"),
		now:          now,
		dial:         dial,
		onChange:     opts.OnChange,
		started:      now(),
		pollInterval: readyPollInterval,
		readyHold:    readyHoldWindow,
		services:     make(map[string]*service, len(cfg.Services)),
	}
	for name, svcCfg := range cfg.Services {
		m.services[name] = &service{
			name:      name,
			cfg:       svcCfg,
			driver:    drivers[name],
			state:     StateUnknown,
			updatedAt: m.started,
		}
	}
	return m
}

func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.services))
	for name := range m.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) Has(name string) bool {
	_, ok := m.services[name]
	return ok
}

func (m *Manager) VisibleTo(orgID, name string) bool {
	return m.Has(name) && slices.Contains(m.cfg.Orgs, orgID)
}

func (m *Manager) Status(name string) (Status, bool) {
	s, ok := m.services[name]
	if !ok {
		return Status{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.snapshot(), true
}

func (m *Manager) List() []Status {
	names := m.Names()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(names))
	for _, name := range names {
		out = append(out, m.services[name].snapshot())
	}
	return out
}

func (m *Manager) ServiceState(name string) (string, bool) {
	s, ok := m.services[name]
	if !ok {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.state, true
}

func (s *service) snapshot() Status {
	return Status{
		Name:         s.name,
		Driver:       s.cfg.Driver,
		State:        s.state,
		Active:       s.active,
		LastActivity: s.lastActivity,
		IdleTimeout:  s.cfg.IdleTimeout,
		StartTimeout: s.cfg.StartTimeout,
		LastError:    s.lastError,
		StartedBy:    s.startedBy,
		UpdatedAt:    s.updatedAt,
	}
}

func (m *Manager) setState(s *service, state string) {
	changed := s.state != state
	s.state = state
	s.updatedAt = m.now()
	if changed && m.onChange != nil {
		m.onChange()
	}
}

func (m *Manager) Acquire(name string) func() {
	s, ok := m.services[name]
	if !ok {
		return func() {}
	}
	m.mu.Lock()
	s.active++
	s.lastActivity = m.now()
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			s.active--
			s.lastActivity = m.now()
			m.mu.Unlock()
		})
	}
}

func (m *Manager) EnsureRunning(ctx context.Context, name, readyAddr string) error {
	call, err := m.beginEnsure(name, readyAddr)
	if err != nil {
		return err
	}
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return fmt.Errorf("waiting for service %q: %w", name, ctx.Err())
	}
}

func (m *Manager) StartAsync(name, readyAddr string) {
	if _, err := m.beginEnsure(name, readyAddr); err != nil {
		m.log.Warn("async start rejected", "service", name, "error", err)
	}
}

func (m *Manager) beginEnsure(name, readyAddr string) (*ensureCall, error) {
	s, ok := m.services[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownService, name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.inflight != nil {
		return s.inflight, nil
	}
	call := &ensureCall{done: make(chan struct{})}
	s.inflight = call
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StartTimeout)
		defer cancel()
		err := m.ensure(ctx, s, readyAddr)
		m.mu.Lock()
		call.err = err
		s.inflight = nil
		m.mu.Unlock()
		close(call.done)
	}()
	return call, nil
}

func (m *Manager) readyTarget(s *service, readyAddr string) string {
	if s.cfg.Ready != nil && s.cfg.Ready.TCP != "" {
		return s.cfg.Ready.TCP
	}
	return hostPortOf(readyAddr)
}

func hostPortOf(target string) string {
	target = strings.TrimSpace(target)
	if i := strings.Index(target, "://"); i >= 0 {
		target = target[i+3:]
	}
	if i := strings.IndexAny(target, "/?#"); i >= 0 {
		target = target[:i]
	}
	if i := strings.LastIndex(target, "@"); i >= 0 {
		target = target[i+1:]
	}
	return target
}

func (m *Manager) probe(ctx context.Context, addr string) error {
	dialCtx, cancel := context.WithTimeout(ctx, readyDialTimeout)
	defer cancel()
	conn, err := m.dial(dialCtx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(m.readyHold)); err != nil {
		return err
	}
	var first [1]byte
	_, err = conn.Read(first[:])
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		return nil
	}
	return fmt.Errorf("%s accepted the connection but closed it, the service is not listening yet: %w", addr, err)
}

func (m *Manager) ensure(ctx context.Context, s *service, readyAddr string) error {
	addr := m.readyTarget(s, readyAddr)

	driverState, stateErr := s.driver.State(ctx)
	if stateErr == nil && addr != "" && (driverState == Running || driverState == Unknown) {
		if m.probe(ctx, addr) == nil {
			m.markRunning(s, false)
			return nil
		}
	}
	if stateErr == nil && addr == "" && driverState == Running {
		m.markRunning(s, false)
		return nil
	}

	m.mu.Lock()
	m.setState(s, StateStarting)
	m.mu.Unlock()

	if err := s.driver.Start(ctx); err != nil {
		return m.failStart(s, fmt.Errorf("start service %q: %w", s.name, err))
	}

	var lastProbeErr error
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		if addr != "" {
			lastProbeErr = m.probe(ctx, addr)
		} else {
			st, err := s.driver.State(ctx)
			if err != nil {
				lastProbeErr = err
			} else if st == Running {
				lastProbeErr = nil
			} else {
				lastProbeErr = fmt.Errorf("driver state is %s", st)
			}
		}
		if lastProbeErr == nil {
			m.markRunning(s, true)
			return nil
		}
		select {
		case <-ctx.Done():
			return m.failStart(s, fmt.Errorf("service %q not ready within %s: %w", s.name, s.cfg.StartTimeout, lastProbeErr))
		case <-ticker.C:
		}
	}
}

func (m *Manager) markRunning(s *service, startedByUs bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setState(s, StateRunning)
	s.lastError = ""
	s.stoppedByUs = false
	if startedByUs {
		s.startedBy = StartedByMishmesh
	} else if s.startedBy == "" {
		s.startedBy = StartedByExternal
	}
	s.lastActivity = m.now()
}

func (m *Manager) failStart(s *service, err error) error {
	m.mu.Lock()
	m.setState(s, StateError)
	s.lastError = err.Error()
	m.mu.Unlock()
	m.log.Warn("service start failed", "service", s.name, "error", err)
	return err
}

func (m *Manager) Stop(ctx context.Context, name string) error {
	s, ok := m.services[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownService, name)
	}
	return m.stopService(ctx, s)
}

func (m *Manager) stopService(ctx context.Context, s *service) error {
	if err := s.driver.Stop(ctx); err != nil {
		err = fmt.Errorf("stop service %q: %w", s.name, err)
		m.mu.Lock()
		s.lastError = err.Error()
		s.updatedAt = m.now()
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	m.setState(s, StateSleeping)
	s.stoppedByUs = true
	s.startedBy = ""
	s.lastError = ""
	m.mu.Unlock()
	return nil
}

func (m *Manager) Run(ctx context.Context) {
	m.Refresh(ctx)
	refresh := time.NewTicker(refreshInterval)
	idle := time.NewTicker(idleCheckInterval)
	defer refresh.Stop()
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			m.Refresh(ctx)
		case <-idle.C:
			m.stopIdle(ctx)
		}
	}
}

func (m *Manager) Refresh(ctx context.Context) {
	for _, name := range m.Names() {
		s := m.services[name]
		callCtx, cancel := context.WithTimeout(ctx, driverCallTimeout)
		state, err := s.driver.State(callCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		m.applyDriverState(s, state, err)
	}
}

func (m *Manager) applyDriverState(s *service, driverState State, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.state == StateStarting {
		return
	}
	if err != nil {
		if s.state == StateUnknown {
			s.lastError = err.Error()
			s.updatedAt = m.now()
		}
		m.log.Warn("service state refresh failed", "service", s.name, "error", err)
		return
	}
	switch driverState {
	case Running:
		if s.state != StateRunning {
			if s.startedBy == "" {
				s.startedBy = StartedByExternal
			}
			s.lastActivity = m.now()
			s.stoppedByUs = false
			s.lastError = ""
			m.setState(s, StateRunning)
		}
	case Stopped:
		next := StateStopped
		if s.stoppedByUs {
			next = StateSleeping
		}
		if s.state == StateError {
			return
		}
		if s.state != next {
			s.startedBy = ""
			m.setState(s, next)
		}
	case Partial:
		if s.state != StatePartial {
			m.setState(s, StatePartial)
		}
	}
}

func (m *Manager) stopIdle(ctx context.Context) {
	now := m.now()
	var candidates []*service
	m.mu.Lock()
	for _, name := range m.Names() {
		s := m.services[name]
		if s.cfg.IdleTimeout <= 0 || s.active > 0 || s.inflight != nil {
			continue
		}
		if s.state != StateRunning && s.state != StatePartial {
			continue
		}
		reference := s.lastActivity
		if reference.IsZero() {
			reference = m.started
		}
		if now.Sub(reference) > s.cfg.IdleTimeout {
			candidates = append(candidates, s)
		}
	}
	m.mu.Unlock()

	for _, s := range candidates {
		callCtx, cancel := context.WithTimeout(ctx, stopTimeout)
		err := m.stopService(callCtx, s)
		cancel()
		if err != nil {
			m.log.Warn("idle stop failed", "service", s.name, "error", err)
			continue
		}
		m.log.Info("service stopped after idle timeout", "service", s.name, "idle_timeout", s.cfg.IdleTimeout)
	}
}
