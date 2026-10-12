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
	done              chan struct{}
	err               error
	addedRequirements []string
}

type service struct {
	def    Definition
	driver Driver

	state           string
	active          int
	lastActivity    time.Time
	lastError       string
	definitionError string
	startedBy       string
	updatedAt       time.Time
	stoppedByUs     bool
	inflight        *ensureCall
	stopping        chan struct{}
	requirements    map[string]func()
	dependents      map[*service]*requirementHold
	version         uint64
	ownActivity     time.Time
	runtimeAlive    bool
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

	applyMu       sync.Mutex
	reloadDone    chan struct{}
	refreshSlot   chan struct{}
	refreshing    bool
	deferredStops map[*service]struct{}
	mu            sync.Mutex
	services      map[string]*service
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
		refreshSlot:  make(chan struct{}, 1),
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
	lastError := s.lastError
	if s.definitionError != "" {
		lastError = s.definitionError
	}
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
		LastError:    lastError,
		StartedBy:    s.startedBy,
		UpdatedAt:    s.updatedAt,
	}
}

func (m *Manager) setState(s *service, state string) bool {
	changed := s.state != state
	s.state = state
	s.updatedAt = m.now()
	return changed
}

func (m *Manager) notify(changed bool) {
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
	s.ownActivity = s.lastActivity
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			s.active--
			s.lastActivity = m.now()
			s.ownActivity = s.lastActivity
			m.mu.Unlock()
		})
	}
}

func (m *Manager) EnsureRunning(ctx context.Context, id, readyAddr string) error {
	return m.ensureRunning(ctx, id, readyAddr, false)
}

func (m *Manager) ensureRunning(ctx context.Context, id, readyAddr string, recursive bool) error {
	call, err := m.beginEnsure(ctx, id, readyAddr, recursive)
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

func (m *Manager) lockAdmission(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		if m.reloadDone == nil {
			return nil
		}
		ready := m.reloadDone
		m.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (m *Manager) StartAsync(id, readyAddr string) {
	go func() {
		if _, err := m.beginEnsure(context.Background(), id, readyAddr, false); err != nil {
			m.log.Warn("async start rejected", "id", id, "error", err)
		}
	}()
}

func (m *Manager) beginEnsure(ctx context.Context, id, readyAddr string, recursive bool) (*ensureCall, error) {
	if recursive {
		m.mu.Lock()
	} else if err := m.lockAdmission(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownService, id)
	}
	if err := m.validateRequirementsLocked(s); err != nil {
		return nil, err
	}
	if s.inflight != nil {
		return s.inflight, nil
	}
	call := &ensureCall{done: make(chan struct{})}
	s.inflight = call
	s.version++
	go m.runEnsure(s, call, s.def, s.driver, readyAddr, s.stopping)
	return call, nil
}

func (m *Manager) runEnsure(s *service, call *ensureCall, def Definition, driver Driver, readyAddr string, stopping <-chan struct{}) {
	if stopping != nil {
		<-stopping
	}
	added, err := m.wakeRequirements(s, def)
	added = append(added, call.addedRequirements...)
	attempted := err == nil
	if err == nil {
		timeout := def.StartTimeout
		if timeout <= 0 {
			timeout = DefaultStartTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err = m.ensure(ctx, s, def, driver, readyAddr)
		cancel()
	}
	retain := false
	failedState := Unknown
	if err != nil && attempted {
		stateCtx, cancel := context.WithTimeout(context.Background(), driverCallTimeout)
		state, stateErr := driver.State(stateCtx)
		cancel()
		retain = stateErr != nil || state != Stopped
		failedState = state
	}
	var release []func()
	m.mu.Lock()
	if err == nil {
		release = m.detachRequirementsLocked(s, def.Requires)
	} else {
		s.lastError = err.Error()
		if failedState == Running || failedState == Partial {
			s.runtimeAlive = true
		}
		if !retain {
			release = m.detachRequirementIDsLocked(s, added)
		}
	}
	m.mu.Unlock()
	for _, done := range release {
		done()
	}
	m.mu.Lock()
	call.err = err
	s.inflight = nil
	s.version++
	close(call.done)
	m.mu.Unlock()
}

func readyTarget(def Definition, readyAddr string) string {
	if def.Ready.TCP != "" {
		return def.Ready.TCP
	}
	if def.Spec.ReadyTCP != "" {
		return def.Spec.ReadyTCP
	}
	if readyAddr != "" {
		return hostPortOf(readyAddr)
	}
	return hostPortOf(def.FallbackReadyAddr)
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
		return tcpProbeError(dialCtx, "TCP readiness connection failed", err)
	}
	defer conn.Close()
	stopCancellation := context.AfterFunc(dialCtx, func() { _ = conn.Close() })
	defer stopCancellation()
	if err := conn.SetReadDeadline(time.Now().Add(m.readyHold)); err != nil {
		return tcpProbeError(dialCtx, "TCP readiness read deadline failed", err)
	}
	var first [1]byte
	_, err = conn.Read(first[:])
	if dialCtx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return tcpProbeError(dialCtx, "TCP readiness read failed", err)
	}
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		return nil
	}
	return tcpProbeError(dialCtx, "TCP readiness connection closed before the service was listening", err)
}

func tcpProbeError(ctx context.Context, diagnostic string, transportError error) error {
	cause := ctx.Err()
	if cause == nil {
		switch {
		case errors.Is(transportError, context.Canceled):
			cause = context.Canceled
		case errors.Is(transportError, context.DeadlineExceeded):
			cause = context.DeadlineExceeded
		}
	}
	if cause != nil {
		return fmt.Errorf("%s: %w", diagnostic, cause)
	}
	return errors.New(diagnostic)
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
	changed := m.setState(s, StateStarting)
	m.mu.Unlock()
	m.notify(changed)

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
	changed := m.setState(s, StateRunning)
	if startedByUs || !s.runtimeAlive {
		s.lastActivity = m.now()
	}
	s.runtimeAlive = true
	s.lastError = ""
	s.stoppedByUs = false
	if startedByUs {
		s.startedBy = StartedByMishmesh
	} else if s.startedBy == "" {
		s.startedBy = StartedByExternal
	}
	m.mu.Unlock()
	m.notify(changed)
}

func (m *Manager) failStart(s *service, err error) error {
	m.mu.Lock()
	changed := m.setState(s, StateError)
	s.lastError = err.Error()
	name, id := s.def.Name, s.def.ID
	m.mu.Unlock()
	m.notify(changed)
	m.log.Warn("service start failed", "service", name, "id", id, "error", err)
	return err
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	stopCtx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	return m.stopServiceIfEligible(stopCtx, nil, false, &id)
}

func (m *Manager) stopServiceIfEligible(ctx context.Context, s *service, idle bool, externalID *string) error {
	for {
		if externalID != nil {
			if err := m.lockAdmission(ctx); err != nil {
				return err
			}
			s = m.services[*externalID]
			if s == nil {
				m.mu.Unlock()
				return fmt.Errorf("%w: %s", ErrUnknownService, *externalID)
			}
		} else {
			m.mu.Lock()
		}
		if m.services[s.def.ID] != s {
			m.mu.Unlock()
			return ErrUnknownService
		}
		if len(s.dependents) > 0 {
			err := &RequirementInUseError{Dependents: m.runningDependentsLocked(s)}
			m.mu.Unlock()
			if idle {
				return nil
			}
			return err
		}
		if idle && !m.idleEligibleLocked(s) {
			m.mu.Unlock()
			return nil
		}
		wait := s.stopping
		if s.inflight != nil {
			wait = s.inflight.done
		}
		if wait != nil {
			m.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		s.stopping = make(chan struct{})
		s.version++
		def, driver := s.def, s.driver
		m.mu.Unlock()
		err := driver.Stop(ctx)
		stopped := err == nil
		if err != nil {
			stateCtx, cancel := context.WithTimeout(context.Background(), driverCallTimeout)
			state, stateErr := driver.State(stateCtx)
			cancel()
			stopped = stateErr == nil && state == Stopped
			err = fmt.Errorf("stop service %q: %w", def.Name, err)
		}
		m.mu.Lock()
		var release []func()
		changed := false
		if stopped {
			changed = m.setState(s, StateSleeping)
			s.stoppedByUs, s.startedBy, s.lastError = true, "", ""
			s.runtimeAlive = false
			release = m.detachRequirementsLocked(s, nil)
		}
		if err != nil {
			s.lastError = err.Error()
			s.updatedAt = m.now()
		}
		close(s.stopping)
		s.stopping = nil
		s.version++
		m.mu.Unlock()
		m.notify(changed)
		for _, done := range release {
			done()
		}
		return err
	}
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

func runtimeProtected(s *service) bool {
	return s.runtimeAlive || s.state == StateRunning || s.state == StatePartial || s.state == StateStarting || s.active > 0 || len(s.requirements) > 0 || len(s.dependents) > 0
}

func driverDefinitionEqual(a, b Definition) bool {
	return a.OrgID == b.OrgID && a.Driver == b.Driver && reflect.DeepEqual(a.Config, b.Config) && reflect.DeepEqual(a.Spec, b.Spec)
}

func (m *Manager) Apply(defs []Definition) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	m.reloadDone = make(chan struct{})
	m.mu.Unlock()
	type prepared struct {
		def    Definition
		driver Driver
		err    error
	}
	updates := make([]prepared, 0, len(defs))
	for _, def := range defs {
		m.mu.Lock()
		existing := m.services[def.ID]
		same := existing != nil && reflect.DeepEqual(existing.def, def)
		m.mu.Unlock()
		if same {
			updates = append(updates, prepared{def: def})
			continue
		}
		driver, err := m.newDriver(def)
		updates = append(updates, prepared{def: def, driver: driver, err: err})
	}
	for {
		m.mu.Lock()
		var wait <-chan struct{}
		for _, s := range m.services {
			if s.inflight != nil {
				wait = s.inflight.done
				break
			}
			if s.stopping != nil {
				wait = s.stopping
				break
			}
		}
		if wait == nil {
			break
		}
		m.mu.Unlock()
		<-wait
	}
	next := make(map[string]*service, len(defs))
	changed := len(defs) != len(m.services)
	var reconcile []*ensureCall
	for _, update := range updates {
		def := update.def
		existing := m.services[def.ID]
		if existing != nil && reflect.DeepEqual(existing.def, def) {
			existing.definitionError = ""
			next[def.ID] = existing
			continue
		}
		changed = true
		if existing != nil && !driverDefinitionEqual(existing.def, def) && runtimeProtected(existing) {
			existing.definitionError = "service driver cannot be replaced while running or held"
			next[def.ID] = existing
			continue
		}
		if existing == nil {
			existing = &service{state: StateUnknown, updatedAt: m.now()}
		}
		existing.def = def
		existing.definitionError = ""
		existing.driver = update.driver
		existing.version++
		if update.err != nil {
			err := fmt.Errorf("service %q: %w", def.Name, update.err)
			existing.driver = brokenDriver{err: err}
			existing.state, existing.lastError = StateError, err.Error()
		} else if existing.state == StateError && len(existing.requirements) == 0 {
			existing.state, existing.lastError = StateUnknown, ""
		}
		next[def.ID] = existing

	}
	for id, s := range m.services {
		if next[id] == nil && runtimeProtected(s) {
			s.definitionError = "service definition cannot be removed while running or held"
			next[id] = s
		}
	}
	m.services = next
	for _, s := range next {
		if s.state != StateRunning && s.state != StatePartial && len(s.requirements) == 0 {
			continue
		}
		if err := m.validateRequirementsLocked(s); err != nil {
			s.lastError = err.Error()
			continue
		}
		if len(s.def.Requires) == 0 && len(s.requirements) == 0 {
			continue
		}
		call := &ensureCall{done: make(chan struct{})}
		s.inflight = call
		s.version++
		reconcile = append(reconcile, call)
		go m.runEnsure(s, call, s.def, s.driver, "", nil)
	}
	close(m.reloadDone)
	m.reloadDone = nil
	m.mu.Unlock()
	m.notify(changed)
	for _, call := range reconcile {
		<-call.done
	}
}

func (m *Manager) Refresh(ctx context.Context) {
	select {
	case m.refreshSlot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-m.refreshSlot }()
	type observation struct {
		svc     *service
		driver  Driver
		version uint64
		state   State
		err     error
	}
	if err := m.lockAdmission(ctx); err != nil {
		return
	}
	m.refreshing = true
	all := make([]observation, 0, len(m.services))
	for _, s := range m.ordered() {
		all = append(all, observation{svc: s, driver: s.driver, version: s.version})
	}
	m.mu.Unlock()
	defer m.finishRefresh()
	for i := range all {
		callCtx, cancel := context.WithTimeout(ctx, driverCallTimeout)
		all[i].state, all[i].err = all[i].driver.State(callCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
	}
	if err := m.lockAdmission(ctx); err != nil {
		return
	}
	var release []func()
	var reconcile []*ensureCall
	changed := false
	for _, item := range all {
		s := item.svc
		if m.services[s.def.ID] != s || s.version != item.version || s.inflight != nil || s.stopping != nil || s.state == StateStarting {
			continue
		}
		if item.err != nil {
			if s.state == StateUnknown {
				s.lastError = item.err.Error()
				s.updatedAt = m.now()
			}
			continue
		}
		switch item.state {
		case Running:
			s.runtimeAlive = true
			if s.state != StateRunning {
				if s.startedBy == "" {
					s.startedBy = StartedByExternal
				}
				s.lastActivity, s.stoppedByUs, s.lastError = m.now(), false, ""
				changed = m.setState(s, StateRunning) || changed
			}
		case Stopped:
			s.runtimeAlive = false
			release = append(release, m.detachRequirementsLocked(s, nil)...)
			if s.state != StateError {
				next := StateStopped
				if s.stoppedByUs {
					next = StateSleeping
				}
				if s.state != next {
					s.startedBy = ""
					changed = m.setState(s, next) || changed
				}
			}
		case Partial:
			s.runtimeAlive = true
			changed = m.setState(s, StatePartial) || changed
		}
		s.version++
	}
	for _, s := range m.ordered() {
		if s.inflight != nil || s.stopping != nil || (s.state != StateRunning && s.state != StatePartial) {
			continue
		}
		if err := m.validateRequirementsLocked(s); err != nil {
			s.lastError = err.Error()
			continue
		}
		var added []string
		for _, id := range s.def.Requires {
			required := m.services[id]
			if required.stopping == nil {
				if m.holdRequirementLocked(s, required) {
					added = append(added, id)
				}
			}
		}
		if len(s.def.Requires) > 0 {
			call := &ensureCall{done: make(chan struct{}), addedRequirements: added}
			s.inflight = call
			s.version++
			reconcile = append(reconcile, call)
			go m.runEnsure(s, call, s.def, s.driver, "", nil)
		}
	}
	m.mu.Unlock()
	m.notify(changed)
	for _, done := range release {
		done()
	}
	for _, call := range reconcile {
		select {
		case <-call.done:
		case <-ctx.Done():
			return
		}
	}
}
