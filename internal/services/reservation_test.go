package services

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestShouldGateInvalidatedRuntimeAndRequirementsUntilSavedReload(t *testing.T) {
	old := Definition{ID: "svc", OrgID: "org", Driver: "fake", Config: json.RawMessage(`{"version":1}`)}
	updated := old
	updated.Config = json.RawMessage(`{"version":2}`)
	oldDriver, newDriver := &fakeDriver{state: Running}, &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if string(def.Config) == string(old.Config) {
			return oldDriver, nil
		}
		return newDriver, nil
	})
	m.Apply([]Definition{old})
	if err := m.Reserve(context.Background(), []Definition{old}, func(r LifecycleReservation) error {
		if err := r.Stop(old.ID); err != nil {
			return err
		}
		r.Invalidate(old.ID)
		return context.Canceled
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m.Refresh(context.Background())
	if m.VisibleTo("org", old.ID) {
		t.Fatal("invalidated service is ingress-visible")
	}
	if err := m.EnsureRunning(context.Background(), old.ID, ""); !errors.Is(err, ErrDefinitionChanged) {
		t.Fatalf("old wake accepted %v", err)
	}
	done := m.Acquire(old.ID)
	done()
	if starts, _ := oldDriver.counts(); starts != 0 {
		t.Fatal("old definition started after commit")
	}
	m.Apply([]Definition{updated})
	if !m.VisibleTo("org", old.ID) {
		t.Fatal("saved reload did not restore availability")
	}
	if err := m.EnsureRunning(context.Background(), old.ID, ""); err != nil {
		t.Fatal(err)
	}
	if starts, _ := oldDriver.counts(); starts != 0 {
		t.Fatal("saved reload woke old driver")
	}
	if starts, _ := newDriver.counts(); starts != 1 {
		t.Fatalf("new driver starts=%d", starts)
	}
}

func TestShouldReserveRestartAcrossConcurrentWakeAcquireAndReload(t *testing.T) {
	old := Definition{ID: "svc", OrgID: "org", Driver: "fake", Config: json.RawMessage(`{"version":1}`), StartTimeout: time.Second}
	updated := old
	updated.Config = json.RawMessage(`{"version":2}`)
	oldDriver, newDriver := &fakeDriver{state: Running}, &fakeDriver{state: Stopped}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if string(def.Config) == string(old.Config) {
			return oldDriver, nil
		}
		return newDriver, nil
	})
	m.Apply([]Definition{old})
	m.Refresh(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	restarted := make(chan error, 1)
	go func() {
		restarted <- m.Reserve(context.Background(), []Definition{old}, func(reservation LifecycleReservation) error {
			if err := reservation.CheckUpdate(old.ID, updated, true); err != nil {
				return err
			}
			if err := reservation.Stop(old.ID); err != nil {
				return err
			}
			close(entered)
			<-release
			if err := reservation.Apply([]Definition{updated}); err != nil {
				return err
			}
			return reservation.Start(old.ID, "")
		})
	}()
	awaitSignal(t, entered)
	woken, reloaded, acquired := make(chan error, 1), make(chan struct{}), make(chan struct{})
	go func() { woken <- m.EnsureRunning(context.Background(), old.ID, "") }()
	go func() { done := m.Acquire(old.ID); close(acquired); done() }()
	go func() { m.Apply([]Definition{old}); close(reloaded) }()
	select {
	case <-acquired:
		t.Fatal("ingress acquired the stopped old definition during save")
	case <-reloaded:
		t.Fatal("reload interleaved the reserved save")
	case <-time.After(30 * time.Millisecond):
	}
	if starts, stops := oldDriver.counts(); starts != 0 || stops != 1 {
		t.Fatalf("old runtime starts=%d stops=%d", starts, stops)
	}
	close(release)
	if err := awaitResult(t, restarted); err != nil {
		t.Fatal(err)
	}
	if err := awaitResult(t, woken); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, acquired)
	awaitSignal(t, reloaded)
	m.mu.Lock()
	config := string(m.services[old.ID].def.Config)
	m.mu.Unlock()
	if config != string(updated.Config) {
		t.Fatalf("stale reload replaced committed definition: %s", config)
	}
	if starts, _ := oldDriver.counts(); starts != 0 {
		t.Fatal("old runtime woke during reserved restart")
	}
	if starts, _ := newDriver.counts(); starts != 1 {
		t.Fatalf("new runtime starts=%d", starts)
	}
}

func TestShouldDrainAdmittedDependentWakeBeforeRejectingRestart(t *testing.T) {
	gate, release := lifecycleGate(t)
	entered := make(chan struct{}, 1)
	base := &fakeDriver{state: Running}
	dependent := &lifecycleDriver{fakeDriver: &fakeDriver{state: Stopped}, beforeStart: func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	defs := []Definition{{ID: "base", OrgID: "org", StartTimeout: time.Second}, {ID: "dependent", OrgID: "org", Requires: []string{"base"}, StartTimeout: time.Second}}
	m := newManager(Options{}, func(def Definition) (Driver, error) {
		if def.ID == "base" {
			return base, nil
		}
		return dependent, nil
	})
	m.Apply(defs)
	wake := make(chan error, 1)
	go func() { wake <- m.EnsureRunning(context.Background(), "dependent", "") }()
	awaitSignal(t, entered)
	restart := make(chan error, 1)
	go func() {
		restart <- m.Reserve(context.Background(), defs[:1], func(reservation LifecycleReservation) error {
			updated := defs[0]
			updated.Config = json.RawMessage(`{"changed":true}`)
			if err := reservation.CheckUpdate("base", updated, true); err != nil {
				return err
			}
			return reservation.Stop("base")
		})
	}()
	release()
	if err := awaitResult(t, wake); err != nil {
		t.Fatal(err)
	}
	var held *RequirementInUseError
	if err := awaitResult(t, restart); !errors.As(err, &held) || len(held.Dependents) != 1 || held.Dependents[0].ID != "dependent" {
		t.Fatalf("restart must reject admitted dependent: %v", err)
	}
	if _, stops := base.counts(); stops != 0 {
		t.Fatal("required runtime stopped before conflict")
	}
}

func TestShouldBoundReservationAdmissionAndRejectStaleRuntimeSnapshots(t *testing.T) {
	driver := &fakeDriver{state: Stopped}
	def := Definition{ID: "svc", OrgID: "org", StartTimeout: time.Second}
	m := newManager(Options{}, func(Definition) (Driver, error) { return driver, nil })
	m.Apply([]Definition{def})
	entered, release := make(chan struct{}), make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		_ = m.Reserve(context.Background(), []Definition{def}, func(LifecycleReservation) error { close(entered); <-release; return nil })
	}()
	awaitSignal(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Reserve(ctx, []Definition{def}, func(LifecycleReservation) error { t.Fatal("cancelled reservation entered"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission timeout=%v", err)
	}
	close(release)
	wait.Wait()
	changed := def
	changed.Ready = Readiness{TCP: "127.0.0.1:8080"}
	m.Apply([]Definition{changed})
	if err := m.Reserve(context.Background(), []Definition{def}, func(LifecycleReservation) error { t.Fatal("stale snapshot entered"); return nil }); !errors.Is(err, ErrDefinitionChanged) {
		t.Fatalf("snapshot rejection=%v", err)
	}
}

func TestShouldRequireRestartForExternalOrUncertainRuntimeUntilExplicitStop(t *testing.T) {
	for _, state := range []State{Running, Unknown, Stopped} {
		t.Run(string(state), func(t *testing.T) {
			driver := &fakeDriver{state: state}
			definition := Definition{ID: "svc", OrgID: "org", Driver: "fake", StartTimeout: time.Second}
			m := newManager(Options{}, func(Definition) (Driver, error) { return driver, nil })
			m.Apply([]Definition{definition})
			proposed := definition
			proposed.StartTimeout = 2 * time.Second
			err := m.Reserve(context.Background(), []Definition{definition}, func(reservation LifecycleReservation) error {
				err := reservation.CheckUpdate("svc", proposed, false)
				if state == Stopped && err != nil {
					t.Fatalf("stopped edit=%v", err)
				}
				if state != Stopped && !errors.Is(err, ErrRestartRequired) {
					t.Fatalf("external/uncertain edit=%v", err)
				}
				if err := reservation.Stop("svc"); err != nil {
					return err
				}
				return reservation.CheckUpdate("svc", proposed, false)
			})
			if err != nil {
				t.Fatalf("explicit stop did not permit edit: %v", err)
			}
		})
	}
}
