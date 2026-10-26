package services

import (
	"context"
	"errors"
	"reflect"
	"time"
)

var ErrDefinitionChanged = errors.New("service definition changed; reload and retry")
var ErrRestartRequired = errors.New("running service configuration changed; stop first or use Save and restart")

type Reservation struct {
	manager *Manager
	context context.Context
}

type LifecycleReservation interface {
	CheckUpdate(string, Definition, bool) error
	Stop(string) error
	Apply([]Definition) error
	Invalidate(string)
	Start(string, string) error
}

func RuntimeDefinitionEqual(a, b Definition) bool {
	return driverDefinitionEqual(a, b) && reflect.DeepEqual(a.Ready, b.Ready) && a.StartTimeout == b.StartTimeout
}

func snapshotDefinitionEqual(a, b Definition) bool {
	a.FallbackReadyAddr, b.FallbackReadyAddr = "", ""
	return reflect.DeepEqual(a, b)
}

func (m *Manager) Reserve(ctx context.Context, expected []Definition, action func(LifecycleReservation) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case m.applySlot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.applySlot }()
	m.mu.Lock()
	m.reloadDone = make(chan struct{})
	m.reserved = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		close(m.reloadDone)
		m.reloadDone = nil
		m.reserved = false
		m.mu.Unlock()
	}()
	if err := m.waitIdleLocked(ctx); err != nil {
		return err
	}
	for _, def := range expected {
		service := m.services[def.ID]
		if service == nil || !snapshotDefinitionEqual(service.def, def) {
			m.mu.Unlock()
			return ErrDefinitionChanged
		}
	}
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return action(&Reservation{manager: m, context: ctx})
}

func (r *Reservation) CheckUpdate(id string, proposed Definition, restart bool) error {
	m := r.manager
	m.mu.Lock()
	service := m.services[id]
	if service == nil {
		m.mu.Unlock()
		return ErrUnknownService
	}
	if RuntimeDefinitionEqual(service.def, proposed) {
		m.mu.Unlock()
		return nil
	}
	driver := service.driver
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.context, driverCallTimeout)
	state, err := driver.State(ctx)
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil && (state == Running || state == Partial) {
		service.runtimeAlive = true
	}
	if len(service.dependents) > 0 {
		return &RequirementInUseError{Dependents: m.runningDependentsLocked(service)}
	}
	if !restart && !service.stoppedByUs && (err != nil || state == Unknown) {
		return ErrRestartRequired
	}
	if runtimeProtected(service) && !restart {
		return ErrRestartRequired
	}
	return nil
}

func (r *Reservation) Stop(id string) error {
	m := r.manager
	m.mu.Lock()
	service := m.services[id]
	m.mu.Unlock()
	if service == nil {
		return ErrUnknownService
	}
	ctx, cancel := context.WithTimeout(r.context, stopTimeout)
	defer cancel()
	return m.stopServiceIfEligible(ctx, service, false, nil)
}

func (r *Reservation) Apply(defs []Definition) error {
	return r.manager.applyReserved(r.context, defs, false)
}

func (r *Reservation) Invalidate(id string) {
	m := r.manager
	m.mu.Lock()
	if service := m.services[id]; service != nil {
		service.invalidated = true
		service.definitionError = "saved service definition requires reload"
		service.version++
		m.applyVersion++
	}
	m.mu.Unlock()
	m.notify(true)
}

func (r *Reservation) Start(id, readyAddr string) error {
	return r.manager.ensureRunning(r.context, id, readyAddr, true)
}
