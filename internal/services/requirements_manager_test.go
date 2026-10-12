package services

import (
	"context"
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

func requirementManager(t *testing.T, drivers map[string]*fakeDriver, defs ...Definition) *Manager {
	t.Helper()
	m := newManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, func(def Definition) (Driver, error) { return drivers[def.ID], nil })
	for i := range defs {
		defs[i].OrgID = "org"
		defs[i].Name = defs[i].ID
		defs[i].StartTimeout = time.Second
	}
	m.pollInterval = time.Millisecond
	m.Apply(defs)
	return m
}

type lifecycleDriver struct {
	*fakeDriver
	beforeStart func(context.Context) error
	beforeStop  func(context.Context) error
	beforeState func(context.Context)
}

func (d *lifecycleDriver) Start(ctx context.Context) error {
	if d.beforeStart != nil {
		if err := d.beforeStart(ctx); err != nil {
			return err
		}
	}
	return d.fakeDriver.Start(ctx)
}

func (d *lifecycleDriver) Stop(ctx context.Context) error {
	if d.beforeStop != nil {
		if err := d.beforeStop(ctx); err != nil {
			return err
		}
	}
	return d.fakeDriver.Stop(ctx)
}

func (d *lifecycleDriver) State(ctx context.Context) (State, error) {
	if d.beforeState != nil {
		d.beforeState(ctx)
	}
	return d.fakeDriver.State(ctx)
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle signal")
	}
}

func awaitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle result")
		return nil
	}
}

func TestShouldWakeRequirementsInParallelBeforeStartingDependent(t *testing.T) {
	entered := make(chan struct{}, 2)
	gate, releaseGate := lifecycleGate(t)
	drivers := map[string]Driver{}
	for _, id := range []string{"a", "b"} {
		drivers[id] = &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped}, beforeStart: func(ctx context.Context) error {
			entered <- struct{}{}
			select {
			case <-gate:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	}
	dependent := &fakeDriver{state: Stopped}
	drivers["c"] = dependent
	m := newManager(Options{}, func(def Definition) (Driver, error) { return drivers[def.ID], nil })
	m.Apply([]Definition{{ID: "a", OrgID: "org", StartTimeout: time.Second}, {ID: "b", OrgID: "org", StartTimeout: time.Second}, {ID: "c", OrgID: "org", Requires: []string{"a", "b"}, StartTimeout: time.Second}})
	result := make(chan error, 1)
	go func() { result <- m.EnsureRunning(context.Background(), "c", "") }()
	awaitSignal(t, entered)
	awaitSignal(t, entered)
	if starts, _ := dependent.counts(); starts != 0 {
		t.Fatal("dependent started before requirements")
	}
	if len(m.RunningDependents("a")) != 1 || len(m.RunningDependents("b")) != 1 {
		t.Fatal("start interval was not protected")
	}
	releaseGate()
	if err := awaitResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestShouldShareDiamondHoldsAcrossConcurrentCallersAndCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	gate, releaseGate := lifecycleGate(t)
	base := &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped}, beforeStart: func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	drivers := map[string]Driver{"base": base, "left": &fakeDriver{state: Stopped}, "right": &fakeDriver{state: Stopped}, "top": &fakeDriver{state: Stopped}}
	m := newManager(Options{}, func(def Definition) (Driver, error) { return drivers[def.ID], nil })
	m.Apply([]Definition{{ID: "base", OrgID: "org", IdleTimeout: -time.Second, StartTimeout: time.Second}, {ID: "left", OrgID: "org", Requires: []string{"base"}, IdleTimeout: -time.Second, StartTimeout: time.Second}, {ID: "right", OrgID: "org", Requires: []string{"base"}, IdleTimeout: -time.Second, StartTimeout: time.Second}, {ID: "top", OrgID: "org", Requires: []string{"left", "right"}, StartTimeout: time.Second}})
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { cancelled <- m.EnsureRunning(ctx, "top", "") }()
	awaitSignal(t, entered)
	cancel()
	if err := awaitResult(t, cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error=%v", err)
	}
	results := make(chan error, 25)
	for range 25 {
		go func() { results <- m.EnsureRunning(context.Background(), "top", "") }()
	}
	releaseGate()
	for range 25 {
		if err := awaitResult(t, results); err != nil {
			t.Fatal(err)
		}
	}
	if starts, _ := base.counts(); starts != 1 {
		t.Fatalf("base starts=%d", starts)
	}
	if got := len(m.RunningDependents("base")); got != 2 {
		t.Fatalf("diamond holds=%d", got)
	}
	status, _ := m.Status("base")
	if status.Active != 0 {
		t.Fatalf("dependency counted as own traffic: %+v", status)
	}
	if err := m.Stop(context.Background(), "top"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"base", "left", "right"} {
		status, _ := m.Status(id)
		if status.State != StateSleeping {
			t.Fatalf("%s did not cascade: %+v", id, status)
		}
	}
}

func TestShouldPreserveHoldsForPartialStartAndFailedStop(t *testing.T) {
	for _, state := range []State{Partial, Running} {
		t.Run(string(state), func(t *testing.T) {
			base := &fakeDriver{state: Stopped}
			dependent := &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped, stopErr: errors.New("busy")}}
			dependent.beforeStart = func(context.Context) error { dependent.setState(state); return errors.New("partial start") }
			m := newManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, func(def Definition) (Driver, error) {
				if def.ID == "base" {
					return base, nil
				}
				return dependent, nil
			})
			m.Apply([]Definition{{ID: "base", OrgID: "org", StartTimeout: time.Second}, {ID: "app", OrgID: "org", Requires: []string{"base"}, StartTimeout: time.Second}})
			if err := m.EnsureRunning(context.Background(), "app", ""); err == nil {
				t.Fatal("expected start error")
			}
			if len(m.RunningDependents("base")) != 1 {
				t.Fatal("partial running workload lost its hold")
			}
			if err := m.Stop(context.Background(), "app"); err == nil {
				t.Fatal("expected stop error")
			}
			if len(m.RunningDependents("base")) != 1 {
				t.Fatal("failed stop lost its hold")
			}
			dependent.mu.Lock()
			dependent.stopErr = nil
			dependent.mu.Unlock()
			if err := m.Stop(context.Background(), "app"); err != nil {
				t.Fatal(err)
			}
			if len(m.RunningDependents("base")) != 0 {
				t.Fatal("successful stop kept its hold")
			}
		})
	}
}

func TestShouldReconcileExternallyRunningRequirementsBeforeIdle(t *testing.T) {
	drivers := map[string]*fakeDriver{"a-base": {state: Running}, "z-app": {state: Running}}
	m := requirementManager(t, drivers, Definition{ID: "a-base", IdleTimeout: -time.Second}, Definition{ID: "z-app", Requires: []string{"a-base"}})
	m.Refresh(context.Background())
	m.stopIdle(context.Background())
	if _, stops := drivers["a-base"].counts(); stops != 0 {
		t.Fatal("externally running requirement stopped")
	}
	drivers["z-app"].setState(Stopped)
	m.Refresh(context.Background())
	if len(m.RunningDependents("a-base")) != 0 {
		t.Fatal("externally stopped dependent kept hold")
	}
	m.stopIdle(context.Background())
	if _, stops := drivers["a-base"].counts(); stops != 1 {
		t.Fatal("unused external requirement did not stop")
	}
}

func TestShouldKeepRecentOwnTrafficGraceAfterLastRequirementRelease(t *testing.T) {
	drivers := map[string]*fakeDriver{"base": {state: Stopped}, "app": {state: Stopped}}
	m := requirementManager(t, drivers, Definition{ID: "base", IdleTimeout: -time.Second}, Definition{ID: "app", Requires: []string{"base"}})
	clock := newFakeClock()
	m.now = clock.Now
	if err := m.EnsureRunning(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
	release := m.Acquire("base")
	if err := m.Stop(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	m.stopIdle(context.Background())
	if _, stops := drivers["base"].counts(); stops != 0 {
		t.Fatal("active traffic stopped")
	}
	release()
	release()
	clock.Advance(29 * time.Second)
	m.stopIdle(context.Background())
	if _, stops := drivers["base"].counts(); stops != 0 {
		t.Fatal("recent traffic stopped")
	}
	clock.Advance(2 * time.Second)
	m.stopIdle(context.Background())
	if _, stops := drivers["base"].counts(); stops != 1 {
		t.Fatal("traffic grace did not expire")
	}
}

func TestShouldUseLinkedTCPFallbackForRequirementReadiness(t *testing.T) {
	probed := make(chan struct{}, 1)
	ready, releaseReady := lifecycleGate(t)
	base := &fakeDriver{state: Running}
	app := &fakeDriver{state: Stopped}
	m := newManager(Options{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "127.0.0.1:4321" {
			return nil, errors.New("wrong fallback")
		}
		select {
		case probed <- struct{}{}:
		default:
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		client, server := net.Pipe()
		go func() { defer server.Close(); _, _ = server.Write([]byte("ready")) }()
		return client, nil
	}}, func(def Definition) (Driver, error) {
		if def.ID == "base" {
			return base, nil
		}
		return app, nil
	})
	m.Apply([]Definition{{ID: "base", OrgID: "org", FallbackReadyAddr: "http://127.0.0.1:4321/path", StartTimeout: time.Second}, {ID: "app", OrgID: "org", Requires: []string{"base"}, StartTimeout: time.Second}})
	result := make(chan error, 1)
	go func() { result <- m.EnsureRunning(context.Background(), "app", "") }()
	awaitSignal(t, probed)
	if starts, _ := app.counts(); starts != 0 {
		t.Fatal("dependent started before linked listener ready")
	}
	releaseReady()
	if err := awaitResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestShouldKeepPriorRequirementsWhenReloadAdditionFails(t *testing.T) {
	drivers := map[string]*fakeDriver{"old": {state: Stopped}, "new": {state: Stopped, startErr: errors.New("offline")}, "app": {state: Stopped}}
	defs := []Definition{{ID: "old", OrgID: "org", Name: "old", IdleTimeout: -time.Second, StartTimeout: time.Second}, {ID: "new", OrgID: "org", Name: "new", StartTimeout: time.Second}, {ID: "app", OrgID: "org", Name: "app", Requires: []string{"old"}, StartTimeout: time.Second}}
	m := requirementManager(t, drivers, defs...)
	if err := m.EnsureRunning(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
	defs[2].Requires = []string{"new"}
	m.Apply(defs)
	if len(m.RunningDependents("old")) != 1 || len(m.RunningDependents("new")) != 0 {
		t.Fatal("failed reload lost prior holds or leaked addition")
	}
	if status, _ := m.Status("app"); status.LastError == "" {
		t.Fatal("reload failure not exposed")
	}
	m.Refresh(context.Background())
	if len(m.RunningDependents("old")) != 1 || len(m.RunningDependents("new")) != 0 {
		t.Fatal("refresh lost prior holds or leaked its failed addition")
	}
	if status, _ := m.Status("app"); status.LastError == "" {
		t.Fatal("refresh hid the failed requirement change")
	}
	if err := m.Stop(context.Background(), "new"); err != nil {
		t.Fatalf("failed addition falsely blocks manual stop: %v", err)
	}
	drivers["new"].mu.Lock()
	drivers["new"].startErr = nil
	drivers["new"].mu.Unlock()
	m.Apply(defs)
	if len(m.RunningDependents("old")) != 0 || len(m.RunningDependents("new")) != 1 {
		t.Fatal("successful reload did not transfer holds")
	}
	if _, stops := drivers["old"].counts(); stops != 1 {
		t.Fatal("old requirement did not stop")
	}
}

func TestShouldGuardRequirementStopWhileDependentStarts(t *testing.T) {
	entered := make(chan struct{})
	gate, releaseGate := lifecycleGate(t)
	base := &lifecycleDriver{fakeDriver: &fakeDriver{state: Running}, beforeStop: func(ctx context.Context) error {
		close(entered)
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	app := &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.ID == "base" {
			return base, nil
		}
		return app, nil
	})
	m.Apply([]Definition{{ID: "base", OrgID: "org", StartTimeout: time.Second}, {ID: "app", OrgID: "org", Requires: []string{"base"}, StartTimeout: time.Second}})
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop(context.Background(), "base") }()
	awaitSignal(t, entered)
	woken := make(chan error, 1)
	go func() { woken <- m.EnsureRunning(context.Background(), "app", "") }()
	releaseGate()
	if err := awaitResult(t, stopped); err != nil {
		t.Fatal(err)
	}
	if err := awaitResult(t, woken); err != nil {
		t.Fatal(err)
	}
	if starts, stops := base.counts(); starts != 1 || stops != 1 {
		t.Fatalf("base starts=%d stops=%d", starts, stops)
	}
	var inUse *RequirementInUseError
	if err := m.Stop(context.Background(), "base"); !errors.As(err, &inUse) || len(inUse.Dependents) != 1 {
		t.Fatalf("manual stop error=%v", err)
	}
}

func TestShouldInvokeCallbacksAndDriverFactoriesWithoutManagerLock(t *testing.T) {
	var m *Manager
	var once sync.Once
	m = newManager(Options{OnChange: func() { m.List() }}, func(Definition) (Driver, error) {
		m.List()
		once.Do(func() {})
		return &fakeDriver{state: Running}, nil
	})
	done := make(chan struct{})
	go func() {
		m.Apply([]Definition{{ID: "x", OrgID: "org", StartTimeout: time.Second}})
		m.Refresh(context.Background())
		close(done)
	}()
	awaitSignal(t, done)
}

func TestShouldIgnoreRefreshObservationAfterDriverReplacement(t *testing.T) {
	entered := make(chan struct{})
	gate, releaseGate := lifecycleGate(t)
	old := &lifecycleDriver{fakeDriver: &fakeDriver{state: Running}, beforeState: func(context.Context) { close(entered); <-gate }}
	replacement := &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.Driver == "old" {
			return old, nil
		}
		return replacement, nil
	})
	def := Definition{ID: "x", OrgID: "org", Driver: "old", StartTimeout: time.Second}
	m.Apply([]Definition{def})
	done := make(chan struct{})
	go func() { m.Refresh(context.Background()); close(done) }()
	awaitSignal(t, entered)
	def.Driver = "new"
	m.Apply([]Definition{def})
	releaseGate()
	awaitSignal(t, done)
	status, _ := m.Status("x")
	if status.Driver != "new" || status.State != StateUnknown {
		t.Fatalf("stale refresh replaced current state: %+v", status)
	}
}

func TestShouldRecheckIdleCandidateAfterTrafficAcquire(t *testing.T) {
	entered := make(chan struct{})
	gate, releaseGate := lifecycleGate(t)
	first := &lifecycleDriver{fakeDriver: &fakeDriver{state: Running}, beforeStop: func(context.Context) error { close(entered); <-gate; return nil }}
	second := &fakeDriver{state: Running}
	clock := newFakeClock()
	m := newManager(Options{Now: clock.Now}, func(def Definition) (Driver, error) {
		if def.ID == "a" {
			return first, nil
		}
		return second, nil
	})
	m.Apply([]Definition{{ID: "a", OrgID: "org", Name: "a", IdleTimeout: time.Second}, {ID: "b", OrgID: "org", Name: "b", IdleTimeout: time.Second}})
	m.Refresh(context.Background())
	clock.Advance(time.Minute)
	done := make(chan struct{})
	go func() { m.stopIdle(context.Background()); close(done) }()
	awaitSignal(t, entered)
	release := m.Acquire("b")
	defer release()
	releaseGate()
	awaitSignal(t, done)
	if _, stops := second.counts(); stops != 0 {
		t.Fatal("stale idle candidate stopped active traffic")
	}
}

func TestShouldProtectRequirementsDuringWholeRefreshBatch(t *testing.T) {
	entered := make(chan struct{})
	gate, releaseGate := lifecycleGate(t)
	base := &fakeDriver{state: Running}
	app := &lifecycleDriver{fakeDriver: &fakeDriver{state: Running}}
	var observations atomic.Int32
	app.beforeState = func(context.Context) {
		if observations.Add(1) == 1 {
			close(entered)
			<-gate
		}
	}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.ID == "a" {
			return base, nil
		}
		return app, nil
	})
	m.Apply([]Definition{{ID: "a", OrgID: "org", Name: "a", IdleTimeout: -time.Second, StartTimeout: time.Second}, {ID: "z", OrgID: "org", Name: "z", Requires: []string{"a"}, StartTimeout: time.Second}})
	done := make(chan struct{})
	go func() { m.Refresh(context.Background()); close(done) }()
	awaitSignal(t, entered)
	m.stopIdle(context.Background())
	if _, stops := base.counts(); stops != 0 {
		t.Fatal("idle ran before external hold reconciliation")
	}
	releaseGate()
	awaitSignal(t, done)
	m.stopIdle(context.Background())
	if _, stops := base.counts(); stops != 0 {
		t.Fatal("required external service stopped")
	}
}

func TestShouldRetainLiveDefinitionsUntilStopped(t *testing.T) {
	old := &fakeDriver{state: Running}
	replacement := &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.Driver == "new" {
			return replacement, nil
		}
		return old, nil
	})
	def := Definition{ID: "x", OrgID: "org", Driver: "old", StartTimeout: time.Second}
	m.Apply([]Definition{def})
	m.Refresh(context.Background())
	def.Driver = "new"
	m.Apply([]Definition{def})
	status, _ := m.Status("x")
	if status.Driver != "old" || status.LastError == "" {
		t.Fatalf("live driver replaced: %+v", status)
	}
	m.Apply(nil)
	if !m.Has("x") {
		t.Fatal("live runtime discarded")
	}
	if err := m.Stop(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	m.Apply([]Definition{def})
	status, _ = m.Status("x")
	if status.Driver != "new" {
		t.Fatal("stopped driver did not accept replacement")
	}
}

func TestShouldRejectForeignRequirementsWithoutDisclosingThem(t *testing.T) {
	foreign := &fakeDriver{state: Stopped}
	app := &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.ID == "secret-id" {
			return foreign, nil
		}
		return app, nil
	})
	m.Apply([]Definition{{ID: "secret-id", Name: "secret-name", OrgID: "foreign", StartTimeout: time.Second}, {ID: "app", OrgID: "org", Requires: []string{"secret-id"}, StartTimeout: time.Second}})
	err := m.EnsureRunning(context.Background(), "app", "")
	if err == nil || strings.Contains(err.Error(), "secret-id") || strings.Contains(err.Error(), "secret-name") {
		t.Fatalf("unsafe error=%v", err)
	}
	if starts, _ := foreign.counts(); starts != 0 {
		t.Fatal("foreign requirement was woken")
	}
}

func TestShouldReleaseHoldsAfterStoppedDependentStartFailure(t *testing.T) {
	drivers := map[string]*fakeDriver{"base": {state: Stopped}, "app": {state: Stopped, startErr: errors.New("offline")}}
	m := requirementManager(t, drivers, Definition{ID: "base", IdleTimeout: -time.Second}, Definition{ID: "app", Requires: []string{"base"}})
	if err := m.EnsureRunning(context.Background(), "app", ""); err == nil {
		t.Fatal("expected start failure")
	}
	if len(m.RunningDependents("base")) != 0 {
		t.Fatal("failed start leaked hold")
	}
	if _, stops := drivers["base"].counts(); stops != 1 {
		t.Fatal("unused requirement did not stop immediately")
	}
}

func TestShouldApplyReadinessPrecedence(t *testing.T) {
	def := Definition{Ready: Readiness{TCP: "127.0.0.1:1"}, FallbackReadyAddr: "http://127.0.0.1:3"}
	if got := readinessTarget(def, "127.0.0.1:2").TCP; got != "127.0.0.1:1" {
		t.Fatal(got)
	}
	def.Ready = Readiness{}
	if got := readinessTarget(def, "127.0.0.1:2").TCP; got != "127.0.0.1:2" {
		t.Fatal(got)
	}
	if got := readinessTarget(def, "").TCP; got != "127.0.0.1:3" {
		t.Fatal(got)
	}
	def.FallbackReadyAddr = ""
	if got := readinessTarget(def, "").TCP; got != "" {
		t.Fatal(got)
	}
}

func TestShouldNotStartDependentAfterRequirementFailure(t *testing.T) {
	required := &fakeDriver{state: Stopped, startErr: errors.New("offline")}
	dependent := &fakeDriver{state: Stopped}
	m := requirementManager(t, map[string]*fakeDriver{"desktop": required, "guacamole": dependent},
		Definition{ID: "desktop"}, Definition{ID: "guacamole", Requires: []string{"desktop"}})
	err := m.EnsureRunning(context.Background(), "guacamole", "")
	starts, _ := dependent.counts()
	if err == nil || !strings.Contains(err.Error(), `requirement "desktop" failed`) || starts != 0 {
		t.Fatalf("starts=%d err=%v", starts, err)
	}
}

func TestShouldKeepRequirementUntilLastDependentStops(t *testing.T) {
	drivers := map[string]*fakeDriver{"base": {state: Stopped}, "a": {state: Stopped}, "b": {state: Stopped}}
	m := requirementManager(t, drivers, Definition{ID: "base", IdleTimeout: -time.Second},
		Definition{ID: "a", Requires: []string{"base"}}, Definition{ID: "b", Requires: []string{"base"}})
	for _, id := range []string{"a", "b"} {
		if err := m.EnsureRunning(context.Background(), id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if starts, _ := drivers["base"].counts(); starts != 1 {
		t.Fatalf("requirement starts=%d", starts)
	}
	if err := m.Stop(context.Background(), "base"); err == nil {
		t.Fatal("stopped a held requirement")
	}
	if err := m.Stop(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, stops := drivers["base"].counts(); stops != 0 {
		t.Fatal("stopped while b runs")
	}
	if err := m.Stop(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if _, stops := drivers["base"].counts(); stops != 1 {
		t.Fatalf("last release stops=%d", stops)
	}
}

func TestShouldRejectCorruptRequirementGraphBeforeWake(t *testing.T) {
	drivers := map[string]*fakeDriver{"a": {state: Stopped}, "b": {state: Stopped}}
	m := requirementManager(t, drivers, Definition{ID: "a", Requires: []string{"b"}}, Definition{ID: "b", Requires: []string{"a"}})
	if err := m.EnsureRunning(context.Background(), "a", ""); err == nil {
		t.Fatal("accepted a cycle")
	}
	for id, driver := range drivers {
		if starts, _ := driver.counts(); starts != 0 {
			t.Fatalf("%s started before validation", id)
		}
	}
}

func lifecycleGate(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return gate, release
}

func TestShouldStartDependentDeadlineAfterRequirementReadiness(t *testing.T) {
	gate, release := lifecycleGate(t)
	base := &fakeDriver{state: Stopped, gate: gate}
	app := &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped}, beforeStart: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 100*time.Millisecond {
			return errors.New("dependent deadline consumed by requirement")
		}
		return ctx.Err()
	}}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.ID == "base" {
			return base, nil
		}
		return app, nil
	})
	m.Apply([]Definition{{ID: "base", OrgID: "org", StartTimeout: time.Second}, {ID: "app", OrgID: "org", Requires: []string{"base"}, StartTimeout: 200 * time.Millisecond}})
	timer := time.AfterFunc(300*time.Millisecond, release)
	defer timer.Stop()
	if err := m.EnsureRunning(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
}

func TestShouldWakeReloadAdditionsBeforeReleasingRemovals(t *testing.T) {
	entered := make(chan struct{})
	gate, release := lifecycleGate(t)
	old := &fakeDriver{state: Stopped}
	added := &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped}, beforeStart: func(ctx context.Context) error {
		close(entered)
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	app := &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		switch def.ID {
		case "old":
			return old, nil
		case "new":
			return added, nil
		default:
			return app, nil
		}
	})
	defs := []Definition{{ID: "old", OrgID: "org", IdleTimeout: -time.Second, StartTimeout: time.Second}, {ID: "new", OrgID: "org", StartTimeout: time.Second}, {ID: "app", OrgID: "org", Requires: []string{"old"}, StartTimeout: time.Second}}
	m.Apply(defs)
	if err := m.EnsureRunning(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
	defs[2].Requires = []string{"new"}
	done := make(chan struct{})
	go func() { m.Apply(defs); close(done) }()
	awaitSignal(t, entered)
	if len(m.RunningDependents("old")) != 1 || len(m.RunningDependents("new")) != 1 {
		t.Fatal("reload interval not protected")
	}
	if _, stops := old.counts(); stops != 0 {
		t.Fatal("old requirement stopped before new readiness")
	}
	release()
	awaitSignal(t, done)
	if _, stops := old.counts(); stops != 1 {
		t.Fatal("old requirement was not released after readiness")
	}
}

func TestShouldExpireTimedIdleDespitePeriodicRequirementRefresh(t *testing.T) {
	drivers := map[string]*fakeDriver{"base": {state: Stopped}, "timed": {state: Stopped}, "app": {state: Stopped}}
	m := requirementManager(t, drivers, Definition{ID: "base", IdleTimeout: -time.Second},
		Definition{ID: "timed", IdleTimeout: time.Minute}, Definition{ID: "app", IdleTimeout: time.Minute, Requires: []string{"base", "timed"}})
	clock := newFakeClock()
	m.now = clock.Now
	if err := m.EnsureRunning(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		clock.Advance(15 * time.Second)
		m.Refresh(context.Background())
		m.stopIdle(context.Background())
	}
	if status, _ := m.Status("app"); status.State != StateSleeping {
		t.Fatalf("periodic refresh prevented dependent idle: %+v", status)
	}
	if _, stops := drivers["base"].counts(); stops != 1 {
		t.Fatalf("With dependents requirement stops=%d", stops)
	}
	for range 5 {
		clock.Advance(15 * time.Second)
		m.Refresh(context.Background())
		m.stopIdle(context.Background())
	}
	if _, stops := drivers["timed"].counts(); stops != 1 {
		t.Fatalf("timed requirement stops=%d", stops)
	}
}

type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *observedWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestShouldCompleteReloadWithoutWaitingForLaterUnrelatedWake(t *testing.T) {
	firstEntered := make(chan struct{})
	firstGate, releaseFirst := lifecycleGate(t)
	laterEntered := make(chan struct{})
	laterGate, releaseLater := lifecycleGate(t)
	factoryEntered := make(chan struct{})
	factoryGate, releaseFactory := lifecycleGate(t)
	drivers := map[string]Driver{
		"first": &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped}, beforeStart: func(ctx context.Context) error {
			close(firstEntered)
			select {
			case <-firstGate:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}},
		"later": &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped}, beforeStart: func(ctx context.Context) error {
			close(laterEntered)
			select {
			case <-laterGate:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}},
	}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.ID == "marker" {
			close(factoryEntered)
			<-factoryGate
			return &fakeDriver{state: Stopped}, nil
		}
		return drivers[def.ID], nil
	})
	defs := []Definition{{ID: "first", OrgID: "org", StartTimeout: 5 * time.Second}, {ID: "later", OrgID: "org", StartTimeout: 5 * time.Second}}
	m.Apply(defs)
	first := make(chan error, 1)
	go func() { first <- m.EnsureRunning(context.Background(), "first", "") }()
	awaitSignal(t, firstEntered)
	applied := make(chan struct{})
	go func() { m.Apply(append(defs, Definition{ID: "marker", OrgID: "org"})); close(applied) }()
	awaitSignal(t, factoryEntered)
	later := make(chan error, 1)
	ctx := &observedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	go func() { later <- m.EnsureRunning(ctx, "later", "") }()
	awaitSignal(t, ctx.waiting)
	releaseFactory()
	releaseFirst()
	if err := awaitResult(t, first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Error("reload waited for a later unrelated wake instead of a finite admitted set")
	}
	awaitSignal(t, laterEntered)
	releaseLater()
	if err := awaitResult(t, later); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, applied)
}

func TestShouldDrainRecursiveWakeAndReleaseCascadeDuringReload(t *testing.T) {
	stopEntered := make(chan struct{})
	stopGate, releaseStop := lifecycleGate(t)
	factoryEntered := make(chan struct{})
	factoryGate, releaseFactory := lifecycleGate(t)
	var stops atomic.Int32
	base := &lifecycleDriver{fakeDriver: &fakeDriver{state: Running}, beforeStop: func(ctx context.Context) error {
		if stops.Add(1) == 1 {
			close(stopEntered)
			select {
			case <-stopGate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	app := &fakeDriver{state: Stopped, startErr: errors.New("offline")}
	m := newManager(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, func(def Definition) (Driver, error) {
		switch def.ID {
		case "base":
			return base, nil
		case "app":
			return app, nil
		default:
			close(factoryEntered)
			<-factoryGate
			return &fakeDriver{state: Stopped}, nil
		}
	})
	defs := []Definition{{ID: "base", OrgID: "org", IdleTimeout: -time.Second, StartTimeout: time.Second}, {ID: "app", OrgID: "org", Requires: []string{"base"}, StartTimeout: time.Second}}
	m.Apply(defs)
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop(context.Background(), "base") }()
	awaitSignal(t, stopEntered)
	ctx := &observedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	woken := make(chan error, 1)
	go func() { woken <- m.EnsureRunning(ctx, "app", "") }()
	awaitSignal(t, ctx.waiting)
	applied := make(chan struct{})
	go func() { m.Apply(append(defs, Definition{ID: "marker", OrgID: "org"})); close(applied) }()
	awaitSignal(t, factoryEntered)
	releaseFactory()
	releaseStop()
	if err := awaitResult(t, stopped); err != nil {
		t.Fatal(err)
	}
	if err := awaitResult(t, woken); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("wake=%v", err)
	}
	awaitSignal(t, applied)
	if starts, count := base.counts(); starts != 1 || count != 2 {
		t.Fatalf("recursive start/release cascade: starts=%d stops=%d", starts, count)
	}
	if len(m.RunningDependents("base")) != 0 {
		t.Fatal("failed recursive wake leaked a hold")
	}
}

func TestShouldCancelExternalLifecycleWaitsDuringReload(t *testing.T) {
	factoryEntered := make(chan struct{})
	factoryGate, releaseFactory := lifecycleGate(t)
	driver := &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.ID == "marker" {
			close(factoryEntered)
			<-factoryGate
		}
		return driver, nil
	})
	defs := []Definition{{ID: "app", OrgID: "org", StartTimeout: time.Second}}
	m.Apply(defs)
	applied := make(chan struct{})
	go func() { m.Apply(append(defs, Definition{ID: "marker", OrgID: "org"})); close(applied) }()
	awaitSignal(t, factoryEntered)
	for _, operation := range []string{"ensure", "stop", "refresh", "refresh-slot"} {
		t.Run(operation, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &observedWaitContext{Context: parent, waiting: make(chan struct{})}
			result := make(chan error, 1)
			if operation == "refresh-slot" {
				m.refreshSlot <- struct{}{}
				defer func() { <-m.refreshSlot }()
			}
			go func() {
				switch operation {
				case "ensure":
					result <- m.EnsureRunning(ctx, "app", "")
				case "stop":
					result <- m.Stop(ctx, "app")
				default:
					m.Refresh(ctx)
					result <- ctx.Err()
				}
			}()
			awaitSignal(t, ctx.waiting)
			cancel()
			if err := awaitResult(t, result); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel=%v", err)
			}
		})
	}
	if starts, stops := driver.counts(); starts != 0 || stops != 0 {
		t.Fatalf("cancelled admission invoked driver: starts=%d stops=%d", starts, stops)
	}
	releaseFactory()
	awaitSignal(t, applied)
}
