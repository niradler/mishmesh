package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
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
	Log              *slog.Logger
	Now              func() time.Time
	Dial             func(ctx context.Context, network, addr string) (net.Conn, error)
	HTTPClient       *http.Client
	OnChange         func()
	DockerHost       string
	HostCommands     bool
	Registry         *Registry
	KubernetesClient KubernetesResolver
}

type Status struct {
	ID           string        `json:"id"`
	OrgID        string        `json:"org_id"`
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
	def    Definition
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

type driverFactory func(Definition) (Driver, error)

type Manager struct {
	log        *slog.Logger
	now        func() time.Time
	dial       func(ctx context.Context, network, addr string) (net.Conn, error)
	httpClient *http.Client
	onChange   func()
	started    time.Time
	newDriver  driverFactory

	pollInterval time.Duration
	readyHold    time.Duration

	mu       sync.Mutex
	services map[string]*service
}

func NewManager(opts Options) *Manager {
	host := opts.DockerHost
	if host == "" {
		host = DefaultDockerHost
	}
	drivers := &hostDrivers{dockerHost: host, hostCommands: opts.HostCommands, registry: opts.Registry, kubernetesClient: opts.KubernetesClient}
	return newManager(opts, drivers.build)
}

type hostDrivers struct {
	dockerHost       string
	hostCommands     bool
	registry         *Registry
	kubernetesClient KubernetesResolver

	mu     sync.Mutex
	client *dockerClient
}

func (h *hostDrivers) docker() (*dockerClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil {
		client, err := newDockerClient(h.dockerHost)
		if err != nil {
			return nil, err
		}
		h.client = client
	}
	return h.client, nil
}

func (h *hostDrivers) build(def Definition) (Driver, error) {
	if h.registry == nil {
		return nil, errors.New("driver registry is required")
	}
	name, config := def.Driver, def.Config
	if len(config) == 0 {
		driverType, legacyConfig, err := legacyDriverConfig(name, def.Spec)
		if err != nil {
			return nil, err
		}
		name, config = driverType.Describe().Type, legacyConfig
	}
	driverType, ok := h.registry.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", name)
	}
	deps := Deps{OrgID: def.OrgID, Docker: h.docker, HostCommands: h.hostCommands, KubernetesClient: h.kubernetesClient}
	if err := validateHostConfig(driverType.Describe(), config, deps); err != nil {
		return nil, err
	}
	if err := driverType.Validate(config); err != nil {
		return nil, err
	}
	return driverType.New(config, deps)
}

type brokenDriver struct{ err error }

func (d brokenDriver) Start(context.Context) error          { return d.err }
func (d brokenDriver) Stop(context.Context) error           { return d.err }
func (d brokenDriver) State(context.Context) (State, error) { return Unknown, d.err }

func newManager(opts Options, factory driverFactory) *Manager {
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
	client := http.DefaultClient
	if opts.HTTPClient != nil {
		client = opts.HTTPClient
	}
	probeClient := *client
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Manager{
		log:          log.With("component", "services"),
		now:          now,
		dial:         dial,
		httpClient:   &probeClient,
		onChange:     opts.OnChange,
		started:      now(),
		newDriver:    factory,
		pollInterval: readyPollInterval,
		readyHold:    readyHoldWindow,
		services:     make(map[string]*service),
	}
}

func (m *Manager) Apply(defs []Definition) {
	next := make(map[string]*service, len(defs))
	m.mu.Lock()
	changed := len(defs) != len(m.services)
	for _, def := range defs {
		existing := m.services[def.ID]
		if existing != nil && reflect.DeepEqual(existing.def, def) {
			next[def.ID] = existing
			continue
		}
		changed = true
		driver, err := m.newDriver(def)
		if existing == nil {
			existing = &service{state: StateUnknown, updatedAt: m.now()}
		}
		existing.def = def
		existing.driver = driver
		if err != nil {
			err = fmt.Errorf("service %q: %w", def.Name, err)
			existing.driver = brokenDriver{err: err}
			existing.state = StateError
			existing.lastError = err.Error()
			existing.updatedAt = m.now()
		} else if existing.state == StateError {
			existing.state = StateUnknown
			existing.lastError = ""
		}
		next[def.ID] = existing
	}
	m.services = next
	m.mu.Unlock()
	if changed && m.onChange != nil {
		m.onChange()
	}
}

func (m *Manager) lookup(id string) (*service, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	return s, ok
}

func (m *Manager) Has(id string) bool {
	_, ok := m.lookup(id)
	return ok
}

func (m *Manager) VisibleTo(orgID, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	return ok && s.def.OrgID == orgID
}

func (m *Manager) Status(id string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return Status{}, false
	}
	return s.snapshot(), true
}

func (m *Manager) List() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.services))
	for _, s := range m.ordered() {
		out = append(out, s.snapshot())
	}
	return out
}

func (m *Manager) ServiceState(id string) (name, state string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return "", "", false
	}
	return s.def.Name, s.state, true
}

func (m *Manager) ordered() []*service {
	out := make([]*service, 0, len(m.services))
	for _, s := range m.services {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].def.OrgID != out[j].def.OrgID {
			return out[i].def.OrgID < out[j].def.OrgID
		}
		if out[i].def.Name != out[j].def.Name {
			return out[i].def.Name < out[j].def.Name
		}
		return out[i].def.ID < out[j].def.ID
	})
	return out
}

func (s *service) snapshot() Status {
	return Status{
		ID:           s.def.ID,
		OrgID:        s.def.OrgID,
		Name:         s.def.Name,
		Driver:       s.def.Driver,
		State:        s.state,
		Active:       s.active,
		LastActivity: s.lastActivity,
		IdleTimeout:  s.def.IdleTimeout,
		StartTimeout: s.def.StartTimeout,
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

func (m *Manager) Acquire(id string) func() {
	m.mu.Lock()
	s, ok := m.services[id]
	if !ok {
		m.mu.Unlock()
		return func() {}
	}
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

func (m *Manager) EnsureRunning(ctx context.Context, id, readyAddr string) error {
	call, err := m.beginEnsure(id, readyAddr)
	if err != nil {
		return err
	}
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return fmt.Errorf("waiting for service %s: %w", id, ctx.Err())
	}
}

func (m *Manager) StartAsync(id, readyAddr string) {
	if _, err := m.beginEnsure(id, readyAddr); err != nil {
		m.log.Warn("async start rejected", "id", id, "error", err)
	}
}

func (m *Manager) beginEnsure(id, readyAddr string) (*ensureCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownService, id)
	}
	if s.inflight != nil {
		return s.inflight, nil
	}
	call := &ensureCall{done: make(chan struct{})}
	s.inflight = call
	def, driver := s.def, s.driver
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), def.StartTimeout)
		defer cancel()
		err := m.ensure(ctx, s, def, driver, readyAddr)
		m.mu.Lock()
		call.err = err
		s.inflight = nil
		m.mu.Unlock()
		close(call.done)
	}()
	return call, nil
}

func readyTarget(def Definition, readyAddr string) string {
	if def.Ready.TCP != "" {
		return def.Ready.TCP
	}
	if def.Spec.ReadyTCP != "" {
		return def.Spec.ReadyTCP
	}
	return hostPortOf(readyAddr)
}

func readinessTarget(def Definition, readyAddr string) Readiness {
	if def.Ready.HTTP != nil {
		return def.Ready
	}
	return Readiness{TCP: readyTarget(def, readyAddr)}
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
	stopCancellation := context.AfterFunc(dialCtx, func() { _ = conn.Close() })
	defer stopCancellation()
	if err := conn.SetReadDeadline(time.Now().Add(m.readyHold)); err != nil {
		return err
	}
	var first [1]byte
	_, err = conn.Read(first[:])
	if dialCtx.Err() != nil {
		return dialCtx.Err()
	}
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		return nil
	}
	return fmt.Errorf("%s accepted the connection but closed it, the service is not listening yet: %w", addr, err)
}

func (m *Manager) ensure(ctx context.Context, s *service, def Definition, driver Driver, readyAddr string) error {
	ready := readinessTarget(def, readyAddr)
	hasTarget := ready.TCP != "" || ready.HTTP != nil

	driverState, stateErr := driver.State(ctx)
	if stateErr == nil && hasTarget && (driverState == Running || driverState == Unknown) {
		if m.probeReadiness(ctx, ready) == nil {
			m.markRunning(s, false)
			return nil
		}
	}
	if stateErr == nil && !hasTarget && driverState == Running {
		m.markRunning(s, false)
		return nil
	}

	m.mu.Lock()
	m.setState(s, StateStarting)
	m.mu.Unlock()

	if err := driver.Start(ctx); err != nil {
		return m.failStart(s, fmt.Errorf("start service %q: %w", def.Name, err))
	}

	var lastProbeErr error
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		if hasTarget {
			lastProbeErr = m.probeReadiness(ctx, ready)
		} else {
			st, err := driver.State(ctx)
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
			return m.failStart(s, fmt.Errorf("service %q not ready within %s: %w", def.Name, def.StartTimeout, lastProbeErr))
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
	name, id := s.def.Name, s.def.ID
	m.mu.Unlock()
	m.log.Warn("service start failed", "service", name, "id", id, "error", err)
	return err
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	s, ok := m.lookup(id)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownService, id)
	}
	return m.stopService(ctx, s)
}

func (m *Manager) stopService(ctx context.Context, s *service) error {
	m.mu.Lock()
	def, driver := s.def, s.driver
	m.mu.Unlock()
	if err := driver.Stop(ctx); err != nil {
		err = fmt.Errorf("stop service %q: %w", def.Name, err)
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
	m.mu.Lock()
	all := m.ordered()
	drivers := make([]Driver, len(all))
	for i, s := range all {
		drivers[i] = s.driver
	}
	m.mu.Unlock()
	for i, s := range all {
		callCtx, cancel := context.WithTimeout(ctx, driverCallTimeout)
		state, err := drivers[i].State(callCtx)
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
		if s.state != StateError {
			m.log.Warn("service state refresh failed", "service", s.def.Name, "id", s.def.ID, "error", err)
		}
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
	type candidate struct {
		svc *service
		def Definition
	}
	var candidates []candidate
	m.mu.Lock()
	for _, s := range m.ordered() {
		if s.def.IdleTimeout <= 0 || s.active > 0 || s.inflight != nil {
			continue
		}
		if s.state != StateRunning && s.state != StatePartial {
			continue
		}
		reference := s.lastActivity
		if reference.IsZero() {
			reference = m.started
		}
		if now.Sub(reference) > s.def.IdleTimeout {
			candidates = append(candidates, candidate{svc: s, def: s.def})
		}
	}
	m.mu.Unlock()

	for _, c := range candidates {
		callCtx, cancel := context.WithTimeout(ctx, stopTimeout)
		err := m.stopService(callCtx, c.svc)
		cancel()
		if err != nil {
			m.log.Warn("idle stop failed", "service", c.def.Name, "id", c.def.ID, "error", err)
			continue
		}
		m.log.Info("service stopped after idle timeout", "service", c.def.Name, "id", c.def.ID, "idle_timeout", c.def.IdleTimeout)
	}
}
