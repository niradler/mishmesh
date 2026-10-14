package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeDriver struct {
	mu       sync.Mutex
	state    State
	stateErr error
	startErr error
	stopErr  error
	noRun    bool
	gate     chan struct{}
	starts   int
	stops    int
}

func (d *fakeDriver) Start(ctx context.Context) error {
	d.mu.Lock()
	d.starts++
	gate := d.gate
	startErr := d.startErr
	noRun := d.noRun
	d.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if startErr != nil {
		return startErr
	}
	if !noRun {
		d.setState(Running)
	}
	return nil
}

func (d *fakeDriver) Stop(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stops++
	if d.stopErr != nil {
		return d.stopErr
	}
	d.state = Stopped
	return nil
}

func (d *fakeDriver) State(context.Context) (State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state, d.stateErr
}

func (d *fakeDriver) setState(s State) {
	d.mu.Lock()
	d.state = s
	d.mu.Unlock()
}

func (d *fakeDriver) counts() (starts, stops int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts, d.stops
}

type managerFixture struct {
	manager *Manager
	driver  *fakeDriver
	clock   *fakeClock
	dials   atomic.Int64
}

func newFixture(t *testing.T, svc Definition, driver *fakeDriver) *managerFixture {
	t.Helper()
	f := &managerFixture{driver: driver, clock: newFakeClock()}
	svc.ID, svc.OrgID, svc.Name = "svc", "org_default", "svc"
	if svc.Driver == "" {
		svc.Driver = DriverExec
	}
	if svc.StartTimeout == 0 {
		svc.StartTimeout = 5 * time.Second
	}
	f.manager = newManager(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: f.clock.Now,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			f.dials.Add(1)
			st, _ := driver.State(ctx)
			if st != Running {
				return nil, errors.New("connection refused")
			}
			client, server := net.Pipe()
			t.Cleanup(func() { _ = server.Close() })
			return client, nil
		},
	}, func(Definition) (Driver, error) { return driver, nil })
	f.manager.Apply([]Definition{svc})
	f.manager.pollInterval = 5 * time.Millisecond
	f.manager.readyHold = 5 * time.Millisecond
	return f
}

func TestEnsureRunningSingleflight(t *testing.T) {
	driver := &fakeDriver{state: Stopped, gate: make(chan struct{})}
	f := newFixture(t, Definition{IdleTimeout: time.Hour}, driver)

	const callers = 25
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- f.manager.EnsureRunning(context.Background(), "svc", "https://127.0.0.1:9443")
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if starts, _ := driver.counts(); starts > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start never called")
		}
		time.Sleep(time.Millisecond)
	}
	if _, state, _ := f.manager.ServiceState("svc"); state != StateStarting {
		t.Errorf("state during start = %s, want starting", state)
	}
	close(driver.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("EnsureRunning: %v", err)
		}
	}
	if starts, _ := driver.counts(); starts != 1 {
		t.Errorf("starts = %d, want 1", starts)
	}
	status, _ := f.manager.Status("svc")
	if status.State != StateRunning || status.StartedBy != StartedByMishmesh {
		t.Errorf("status = %+v", status)
	}
}

func TestEnsureRunningAlreadyRunningSkipsStart(t *testing.T) {
	driver := &fakeDriver{state: Running}
	f := newFixture(t, Definition{}, driver)
	if err := f.manager.EnsureRunning(context.Background(), "svc", "127.0.0.1:80"); err != nil {
		t.Fatal(err)
	}
	if starts, _ := driver.counts(); starts != 0 {
		t.Errorf("starts = %d, want 0", starts)
	}
	status, _ := f.manager.Status("svc")
	if status.State != StateRunning || status.StartedBy != StartedByExternal {
		t.Errorf("status = %+v", status)
	}
}

func TestEnsureRunningReadinessTargets(t *testing.T) {
	tests := []struct {
		name      string
		readyTCP  string
		readyAddr string
		wantDial  bool
	}{
		{"uses endpoint addr", "", "http://127.0.0.1:8080/path", true},
		{"ready spec wins", "127.0.0.1:9", "127.0.0.1:1", true},
		{"no address relies on driver state", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &fakeDriver{state: Stopped}
			f := newFixture(t, Definition{Spec: Spec{ReadyTCP: tt.readyTCP}}, driver)
			if err := f.manager.EnsureRunning(context.Background(), "svc", tt.readyAddr); err != nil {
				t.Fatal(err)
			}
			if dialed := f.dials.Load() > 0; dialed != tt.wantDial {
				t.Errorf("dialed = %v, want %v", dialed, tt.wantDial)
			}
		})
	}
}

func TestEnsureRunningReadinessTimeout(t *testing.T) {
	driver := &fakeDriver{state: Stopped}
	f := newFixture(t, Definition{StartTimeout: 150 * time.Millisecond}, driver)
	f.manager.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	err := f.manager.EnsureRunning(context.Background(), "svc", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	_, state, _ := f.manager.ServiceState("svc")
	if state != StateError {
		t.Errorf("state = %s, want error", state)
	}
	status, _ := f.manager.Status("svc")
	if status.LastError == "" {
		t.Error("LastError empty")
	}
}

func TestEnsureRunningStartFailure(t *testing.T) {
	driver := &fakeDriver{state: Stopped, startErr: errors.New("no such image")}
	f := newFixture(t, Definition{}, driver)
	err := f.manager.EnsureRunning(context.Background(), "svc", "127.0.0.1:1")
	if err == nil || !errors.Is(err, driver.startErr) {
		t.Fatalf("err = %v", err)
	}
	status, _ := f.manager.Status("svc")
	if status.State != StateError || status.LastError == "" {
		t.Errorf("status = %+v", status)
	}

	driver.mu.Lock()
	driver.startErr = nil
	driver.mu.Unlock()
	if err := f.manager.EnsureRunning(context.Background(), "svc", "127.0.0.1:1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	status, _ = f.manager.Status("svc")
	if status.State != StateRunning || status.LastError != "" {
		t.Errorf("status after retry = %+v", status)
	}
}

func TestEnsureRunningCallerContextCancel(t *testing.T) {
	driver := &fakeDriver{state: Stopped, gate: make(chan struct{})}
	f := newFixture(t, Definition{}, driver)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := f.manager.EnsureRunning(ctx, "svc", "127.0.0.1:1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(driver.gate)
	if err := f.manager.EnsureRunning(context.Background(), "svc", "127.0.0.1:1"); err != nil {
		t.Fatalf("second caller: %v", err)
	}
	if starts, _ := driver.counts(); starts != 1 {
		t.Errorf("starts = %d, want 1", starts)
	}
}

func TestEnsureRunningUnknownService(t *testing.T) {
	f := newFixture(t, Definition{}, &fakeDriver{})
	if err := f.manager.EnsureRunning(context.Background(), "nope", ""); !errors.Is(err, ErrUnknownService) {
		t.Errorf("err = %v", err)
	}
	if err := f.manager.Stop(context.Background(), "nope"); !errors.Is(err, ErrUnknownService) {
		t.Errorf("err = %v", err)
	}
	f.manager.Acquire("nope")()
}

func TestStartAsync(t *testing.T) {
	driver := &fakeDriver{state: Stopped}
	f := newFixture(t, Definition{}, driver)
	f.manager.StartAsync("svc", "127.0.0.1:1")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, state, _ := f.manager.ServiceState("svc"); state == StateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service never became running")
		}
		time.Sleep(time.Millisecond)
	}
	f.manager.StartAsync("missing", "")
}

func TestManualStopSleeps(t *testing.T) {
	driver := &fakeDriver{state: Stopped}
	f := newFixture(t, Definition{}, driver)
	ctx := context.Background()
	if err := f.manager.EnsureRunning(ctx, "svc", "127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Stop(ctx, "svc"); err != nil {
		t.Fatal(err)
	}
	if _, state, _ := f.manager.ServiceState("svc"); state != StateSleeping {
		t.Errorf("state = %s, want sleeping", state)
	}
	f.manager.Refresh(ctx)
	if _, state, _ := f.manager.ServiceState("svc"); state != StateSleeping {
		t.Errorf("state after refresh = %s, want sleeping", state)
	}
	status, _ := f.manager.Status("svc")
	if status.StartedBy != "" {
		t.Errorf("StartedBy = %q", status.StartedBy)
	}
}

func TestManualStopFailure(t *testing.T) {
	driver := &fakeDriver{state: Running, stopErr: errors.New("busy")}
	f := newFixture(t, Definition{}, driver)
	f.manager.Refresh(context.Background())
	if err := f.manager.Stop(context.Background(), "svc"); err == nil {
		t.Fatal("expected error")
	}
	status, _ := f.manager.Status("svc")
	if status.State != StateRunning || status.LastError == "" {
		t.Errorf("status = %+v", status)
	}
}

func TestIdleStop(t *testing.T) {
	driver := &fakeDriver{state: Stopped}
	f := newFixture(t, Definition{IdleTimeout: 10 * time.Minute}, driver)
	ctx := context.Background()
	if err := f.manager.EnsureRunning(ctx, "svc", "127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}

	f.clock.Advance(9 * time.Minute)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 0 {
		t.Fatalf("stopped before idle timeout")
	}

	f.clock.Advance(2 * time.Minute)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 1 {
		t.Fatalf("stops = %d, want 1", stops)
	}
	if _, state, _ := f.manager.ServiceState("svc"); state != StateSleeping {
		t.Errorf("state = %s, want sleeping", state)
	}

	f.clock.Advance(time.Hour)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 1 {
		t.Errorf("stopped again while sleeping")
	}
}

func TestIdleDisabledNeverStops(t *testing.T) {
	driver := &fakeDriver{state: Running}
	f := newFixture(t, Definition{IdleTimeout: 0}, driver)
	f.manager.Refresh(context.Background())
	f.clock.Advance(1000 * time.Hour)
	f.manager.stopIdle(context.Background())
	if _, stops := driver.counts(); stops != 0 {
		t.Errorf("stops = %d", stops)
	}
}

func TestActiveConnectionPreventsStop(t *testing.T) {
	driver := &fakeDriver{state: Running}
	f := newFixture(t, Definition{IdleTimeout: 10 * time.Minute}, driver)
	ctx := context.Background()
	f.manager.Refresh(ctx)

	release := f.manager.Acquire("svc")
	f.clock.Advance(time.Hour)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 0 {
		t.Fatal("stopped with active connection")
	}
	status, _ := f.manager.Status("svc")
	if status.Active != 1 {
		t.Errorf("active = %d", status.Active)
	}

	release()
	release()
	status, _ = f.manager.Status("svc")
	if status.Active != 0 {
		t.Errorf("active after double release = %d", status.Active)
	}

	f.clock.Advance(5 * time.Minute)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 0 {
		t.Fatal("stopped before grace after release")
	}
	f.clock.Advance(6 * time.Minute)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 1 {
		t.Fatalf("stops = %d, want 1", stops)
	}
}

func TestIdleGraceForUnseenService(t *testing.T) {
	driver := &fakeDriver{state: Running}
	f := newFixture(t, Definition{IdleTimeout: 10 * time.Minute}, driver)
	f.manager.mu.Lock()
	f.manager.services["svc"].state = StateRunning
	f.manager.mu.Unlock()
	ctx := context.Background()

	f.clock.Advance(9 * time.Minute)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 0 {
		t.Fatal("stopped inside grace period")
	}
	f.clock.Advance(2 * time.Minute)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 1 {
		t.Fatalf("stops = %d, want 1", stops)
	}
}

func TestExternallyStartedServiceGetsFullGrace(t *testing.T) {
	driver := &fakeDriver{state: Stopped}
	f := newFixture(t, Definition{IdleTimeout: 10 * time.Minute}, driver)
	ctx := context.Background()
	f.manager.Refresh(ctx)
	if _, state, _ := f.manager.ServiceState("svc"); state != StateStopped {
		t.Fatalf("state = %s, want stopped", state)
	}

	f.clock.Advance(time.Hour)
	driver.setState(Running)
	f.manager.Refresh(ctx)
	status, _ := f.manager.Status("svc")
	if status.State != StateRunning || status.StartedBy != StartedByExternal {
		t.Fatalf("status = %+v", status)
	}
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 0 {
		t.Fatal("stopped externally started service immediately")
	}
	f.clock.Advance(11 * time.Minute)
	f.manager.stopIdle(ctx)
	if _, stops := driver.counts(); stops != 1 {
		t.Fatalf("stops = %d, want 1", stops)
	}
}

func TestRefreshStateMapping(t *testing.T) {
	tests := []struct {
		name        string
		initial     string
		stoppedByUs bool
		driver      State
		driverErr   error
		want        string
	}{
		{"running", StateUnknown, false, Running, nil, StateRunning},
		{"stopped", StateRunning, false, Stopped, nil, StateStopped},
		{"stopped by us is sleeping", StateRunning, true, Stopped, nil, StateSleeping},
		{"partial", StateRunning, false, Partial, nil, StatePartial},
		{"starting untouched", StateStarting, false, Running, nil, StateStarting},
		{"error kept on stopped", StateError, false, Stopped, nil, StateError},
		{"error recovers when running", StateError, false, Running, nil, StateRunning},
		{"unknown driver state keeps state", StateRunning, false, Unknown, nil, StateRunning},
		{"driver error keeps state", StateRunning, false, Unknown, errors.New("socket gone"), StateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := &fakeDriver{state: tt.driver, stateErr: tt.driverErr}
			f := newFixture(t, Definition{}, driver)
			f.manager.mu.Lock()
			svc := f.manager.services["svc"]
			svc.state = tt.initial
			svc.stoppedByUs = tt.stoppedByUs
			f.manager.mu.Unlock()
			f.manager.Refresh(context.Background())
			if _, got, _ := f.manager.ServiceState("svc"); got != tt.want {
				t.Errorf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRunLoopStopsOnCancel(t *testing.T) {
	driver := &fakeDriver{state: Running}
	f := newFixture(t, Definition{}, driver)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.manager.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, state, _ := f.manager.ServiceState("svc"); state == StateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial refresh did not run")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestVisibilityAndListing(t *testing.T) {
	clock := newFakeClock()
	m := newManager(Options{Now: clock.Now}, func(Definition) (Driver, error) { return &fakeDriver{}, nil })
	m.Apply([]Definition{
		{ID: "svc_z", OrgID: "org_b", Name: "zeta", Driver: DriverExec, StartTimeout: time.Minute},
		{ID: "svc_a", OrgID: "org_a", Name: "alpha", Driver: DriverExec, StartTimeout: time.Minute, IdleTimeout: 90 * time.Second},
	})

	tests := []struct {
		org, id string
		want    bool
	}{
		{"org_a", "svc_a", true},
		{"org_b", "svc_z", true},
		{"org_b", "svc_a", false},
		{"org_c", "svc_a", false},
		{"org_a", "missing", false},
	}
	for _, tt := range tests {
		if got := m.VisibleTo(tt.org, tt.id); got != tt.want {
			t.Errorf("VisibleTo(%s,%s) = %v", tt.org, tt.id, got)
		}
	}
	if m.Has("missing") || !m.Has("svc_a") {
		t.Error("Has wrong")
	}
	if _, ok := m.Status("missing"); ok {
		t.Error("Status(missing) ok")
	}
	if _, _, ok := m.ServiceState("missing"); ok {
		t.Error("ServiceState(missing) ok")
	}
	list := m.List()
	if len(list) != 2 || list[0].ID != "svc_a" || list[1].ID != "svc_z" {
		t.Fatalf("List = %+v", list)
	}
	if list[0].State != StateUnknown || list[0].OrgID != "org_a" || list[0].Name != "alpha" {
		t.Errorf("first entry = %+v", list[0])
	}
}

func TestApplyKeepsRuntimeStateAndDropsRemoved(t *testing.T) {
	builds := 0
	driver := &fakeDriver{state: Running}
	m := newManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, func(Definition) (Driver, error) {
		builds++
		return driver, nil
	})
	def := Definition{ID: "svc_1", OrgID: "org_a", Name: "web", Driver: DriverExec, StartTimeout: time.Minute}
	m.Apply([]Definition{def})
	m.Refresh(context.Background())
	release := m.Acquire("svc_1")
	defer release()

	m.Apply([]Definition{def})
	if builds != 1 {
		t.Fatalf("unchanged definition rebuilt the driver: %d builds", builds)
	}
	renamed := def
	renamed.Name = "website"
	renamed.IdleTimeout = time.Hour
	m.Apply([]Definition{renamed})
	if builds != 1 {
		t.Fatalf("metadata-only edit rebuilt the runtime driver: %d builds", builds)
	}
	st, ok := m.Status("svc_1")
	if !ok || st.State != StateRunning || st.Active != 1 || st.Name != "website" || st.IdleTimeout != time.Hour {
		t.Fatalf("runtime state lost on update: %+v", st)
	}

	m.Apply(nil)
	if !m.Has("svc_1") {
		t.Fatal("live definition silently discarded")
	}
	release()
	if err := m.Stop(context.Background(), "svc_1"); err != nil {
		t.Fatal(err)
	}
	m.Apply(nil)
	if m.Has("svc_1") {
		t.Fatal("removed definition still present")
	}
}

func TestApplyMarksBrokenDefinitions(t *testing.T) {
	m := newManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, func(def Definition) (Driver, error) {
		if def.Driver == DriverExec {
			return nil, ErrHostCommandsOff
		}
		return &fakeDriver{}, nil
	})
	m.Apply([]Definition{{ID: "svc_x", OrgID: "org_a", Name: "x", Driver: DriverExec, StartTimeout: time.Second}})
	st, _ := m.Status("svc_x")
	if st.State != StateError || !strings.Contains(st.LastError, "disabled") {
		t.Fatalf("status = %+v", st)
	}
	if err := m.EnsureRunning(context.Background(), "svc_x", ""); !errors.Is(err, ErrHostCommandsOff) {
		t.Fatalf("EnsureRunning err = %v", err)
	}

	m.Apply([]Definition{{ID: "svc_x", OrgID: "org_a", Name: "x", Driver: DriverDocker, StartTimeout: time.Second}})
	if st, _ := m.Status("svc_x"); st.State != StateUnknown || st.LastError != "" {
		t.Fatalf("fixed definition still broken: %+v", st)
	}
}

func TestStatusJSON(t *testing.T) {
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	status := Status{
		ID: "svc_1", OrgID: "org_a", Name: "x", Driver: "docker", State: StateRunning, Active: 2,
		LastActivity: at, IdleTimeout: 30 * time.Minute, StartTimeout: 2 * time.Minute,
		LastError: "e", StartedBy: StartedByMishmesh, UpdatedAt: at,
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "svc_1", "org_id": "org_a", "name": "x", "driver": "docker", "state": "running", "active": float64(2),
		"last_activity": "2026-10-12T08:00:00Z", "idle_timeout_seconds": float64(1800),
		"start_timeout_seconds": float64(120), "last_error": "e", "started_by": "mishmesh",
		"updated_at": "2026-10-12T08:00:00Z",
	}
	if len(got) != len(want) {
		t.Errorf("keys = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}

	raw, _ = json.Marshal(Status{Name: "y"})
	got = nil
	_ = json.Unmarshal(raw, &got)
	for _, absent := range []string{"last_activity", "last_error", "started_by"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s should be omitted when empty", absent)
		}
	}
}

func TestHostPortOf(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://127.0.0.1:9443", "127.0.0.1:9443"},
		{"http://127.0.0.1:8080/some/path?q=1", "127.0.0.1:8080"},
		{"127.0.0.1:80", "127.0.0.1:80"},
		{"http://user:pw@host:81/x", "host:81"},
		{"", ""},
		{"  ", ""},
	}
	for _, tt := range tests {
		if got := hostPortOf(tt.in); got != tt.want {
			t.Errorf("hostPortOf(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHostDriversGateHostCommands(t *testing.T) {
	tests := []struct {
		name         string
		def          Definition
		hostCommands bool
		wantErr      error
	}{
		{"exec blocked", Definition{Driver: DriverExec, Spec: Spec{Exec: &ExecSpec{Start: []string{"x"}, Stop: []string{"y"}}}}, false, ErrHostCommandsOff},
		{"exec allowed", Definition{Driver: DriverExec, Spec: Spec{Exec: &ExecSpec{Start: []string{"x"}, Stop: []string{"y"}}}}, true, nil},
		{"compose file blocked", Definition{Driver: DriverCompose, Spec: Spec{Compose: &ComposeSpec{Project: "p", File: "/x.yml", Bin: "docker"}}}, false, ErrHostCommandsOff},
		{"compose by label allowed", Definition{Driver: DriverCompose, Spec: Spec{Compose: &ComposeSpec{Project: "p"}}}, false, nil},
		{"docker allowed", Definition{Driver: DriverDocker, Spec: Spec{Docker: &DockerSpec{Containers: []string{"a"}}}}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &hostDrivers{dockerHost: DefaultDockerHost, hostCommands: tt.hostCommands, registry: testRegistry(t)}
			_, err := h.build(tt.def)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
	bad := &hostDrivers{dockerHost: "ftp://nope", registry: testRegistry(t)}
	if _, err := bad.build(Definition{Driver: DriverDocker, Spec: Spec{Docker: &DockerSpec{Label: "a"}}}); err == nil {
		t.Error("expected error for bad docker host")
	}
}

func TestManagerWithDockerEngine(t *testing.T) {
	engine := newFakeEngine(t, &fakeContainer{ID: "1", Name: "portainer", State: "exited"})
	m := NewManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DockerHost: engine.host(), Registry: testRegistry(t)})
	m.Apply([]Definition{{ID: "svc_p", OrgID: "org_a", Name: "portainer", Driver: DriverDocker,
		Spec: Spec{Docker: &DockerSpec{Containers: []string{"portainer"}}}, StartTimeout: 5 * time.Second}})
	if err := m.EnsureRunning(context.Background(), "svc_p", ""); err != nil {
		t.Fatal(err)
	}
	if engine.stateOf("1") != "running" {
		t.Error("container not running")
	}
	if err := m.Stop(context.Background(), "svc_p"); err != nil {
		t.Fatal(err)
	}
	if engine.stateOf("1") != "exited" {
		t.Error("container not stopped")
	}
	if _, state, _ := m.ServiceState("svc_p"); state != StateSleeping {
		t.Errorf("state = %s", state)
	}
}

func TestProbeRequiresListeningService(t *testing.T) {
	tests := []struct {
		name      string
		serve     func(net.Conn)
		wantReady bool
	}{
		{"accept then close like a docker proxy without backend", func(c net.Conn) { _ = c.Close() }, false},
		{"quiet listener holding the connection", func(c net.Conn) { time.Sleep(time.Second); _ = c.Close() }, true},
		{"server speaks first", func(c net.Conn) { _, _ = c.Write([]byte("SSH-2.0-x\r\n")); time.Sleep(time.Second); _ = c.Close() }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					go tc.serve(c)
				}
			}()
			m := newManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, nil)
			m.readyHold = 200 * time.Millisecond
			err = m.probe(context.Background(), ln.Addr().String())
			if (err == nil) != tc.wantReady {
				t.Fatalf("probe err = %v, want ready=%v", err, tc.wantReady)
			}
		})
	}
}

func TestOnChangeFiresOnStateTransitionsOnly(t *testing.T) {
	driver := &fakeDriver{state: Running}
	f := newFixture(t, Definition{}, driver)
	var changes atomic.Int32
	f.manager.onChange = func() { changes.Add(1) }
	ctx := context.Background()
	f.manager.Refresh(ctx)
	f.manager.Refresh(ctx)
	if got := changes.Load(); got != 1 {
		t.Fatalf("unknown to running then running again: %d changes, want 1", got)
	}
	driver.setState(Stopped)
	f.manager.Refresh(ctx)
	if got := changes.Load(); got != 2 {
		t.Fatalf("running to stopped: %d changes, want 2", got)
	}
}
