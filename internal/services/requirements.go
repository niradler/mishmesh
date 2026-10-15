package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func ValidateRequirements(orgID, serviceID string, requires []string, services []*store.Service) error {
	return store.ValidateRequirements(orgID, serviceID, requires, services)
}

type requirementHold struct{ present bool }

type RequirementInUseError struct {
	Dependents []Status
}

func (e *RequirementInUseError) Error() string {
	names := make([]string, 0, len(e.Dependents))
	for _, dependent := range e.Dependents {
		names = append(names, fmt.Sprintf("%s (%s)", dependent.Name, dependent.State))
	}
	return "required by " + strings.Join(names, ", ")
}

func (m *Manager) RunningDependents(id string) []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.services[id]; s != nil {
		return m.runningDependentsLocked(s)
	}
	return nil
}

func (m *Manager) runningDependentsLocked(s *service) []Status {
	result := make([]Status, 0, len(s.dependents))
	for dependent := range s.dependents {
		if dependent.def.OrgID == s.def.OrgID {
			result = append(result, dependent.snapshot())
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func (m *Manager) validateRequirementsLocked(s *service) error {
	if len(s.def.Requires) == 0 {
		return nil
	}
	records := make([]*store.Service, 0, len(m.services))
	for _, candidate := range m.services {
		if candidate.invalidated {
			continue
		}
		records = append(records, &store.Service{ID: candidate.def.ID, OrgID: candidate.def.OrgID, Name: candidate.def.Name, Requires: candidate.def.Requires})
	}
	return ValidateRequirements(s.def.OrgID, s.def.ID, s.def.Requires, records)
}

func (m *Manager) holdRequirementLocked(owner, required *service) bool {
	if owner.requirements == nil {
		owner.requirements = make(map[string]func())
	}
	if _, exists := owner.requirements[required.def.ID]; exists {
		return false
	}
	if required.dependents == nil {
		required.dependents = make(map[*service]*requirementHold)
	}
	hold := &requirementHold{}
	required.dependents[owner] = hold
	var once sync.Once
	owner.requirements[required.def.ID] = func() {
		once.Do(func() {
			m.mu.Lock()
			if required.dependents[owner] != hold {
				m.mu.Unlock()
				return
			}
			delete(required.dependents, owner)
			required.lastActivity = m.now()
			stop := required.def.IdleTimeout == -time.Second && m.idleEligibleLocked(required)
			if required.def.IdleTimeout == -time.Second && m.refreshing {
				if m.deferredStops == nil {
					m.deferredStops = make(map[*service]struct{})
				}
				m.deferredStops[required] = struct{}{}
			}
			m.mu.Unlock()
			if stop {
				ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
				defer cancel()
				if err := m.stopServiceIfEligible(ctx, required, true, nil); err != nil {
					m.log.Warn("requirement idle stop failed", "error", err)
				}
			}
		})
	}
	return true
}

func (m *Manager) wakeRequirements(owner *service, def Definition) ([]string, error) {
	type result struct {
		id    string
		added bool
		err   error
	}
	results := make(chan result, len(def.Requires))
	unique := make(map[string]bool)
	for _, id := range def.Requires {
		if unique[id] {
			continue
		}
		unique[id] = true
		go func() {
			added := false
			for {
				m.mu.Lock()
				required := m.services[id]
				if required == nil || required.def.OrgID != def.OrgID {
					m.mu.Unlock()
					results <- result{id: id, err: errors.New("requirement must reference a service in this organization")}
					return
				}
				if required.stopping != nil {
					wait := required.stopping
					m.mu.Unlock()
					<-wait
					continue
				}
				added = m.holdRequirementLocked(owner, required)
				name := required.def.Name
				m.mu.Unlock()
				err := m.ensureRunning(context.Background(), id, "", true)
				if err != nil {
					err = fmt.Errorf("requirement %q failed: %w", name, err)
				}
				results <- result{id: id, added: added, err: err}
				return
			}
		}()
	}
	var added []string
	var resultErr error
	for range unique {
		item := <-results
		if item.added {
			added = append(added, item.id)
		}
		if item.err != nil {
			resultErr = errors.Join(resultErr, item.err)
		}
	}
	return added, resultErr
}

func (m *Manager) detachRequirementIDsLocked(s *service, ids []string) []func() {
	var release []func()
	for _, id := range ids {
		if done := s.requirements[id]; done != nil {
			release = append(release, done)
			delete(s.requirements, id)
		}
	}
	return release
}

func (m *Manager) detachRequirementsLocked(s *service, keep []string) []func() {
	wanted := make(map[string]bool, len(keep))
	for _, id := range keep {
		wanted[id] = true
	}
	var ids []string
	for id := range s.requirements {
		if !wanted[id] {
			ids = append(ids, id)
		}
	}
	return m.detachRequirementIDsLocked(s, ids)
}

func (m *Manager) idleEligibleLocked(s *service) bool {
	if m.refreshing || s.active > 0 || len(s.dependents) > 0 || s.inflight != nil || s.stopping != nil {
		return false
	}
	if s.state != StateRunning && s.state != StatePartial && !s.runtimeAlive {
		return false
	}
	if s.def.IdleTimeout == -time.Second {
		return s.ownActivity.IsZero() || m.now().Sub(s.ownActivity) > idleCheckInterval
	}
	if s.def.IdleTimeout <= 0 {
		return false
	}
	reference := s.lastActivity
	if reference.IsZero() {
		reference = m.started
	}
	return m.now().Sub(reference) > s.def.IdleTimeout
}

func (m *Manager) stopIdle(ctx context.Context) {
	m.mu.Lock()
	if m.reloadDone != nil {
		m.mu.Unlock()
		return
	}
	var candidates []*service
	for _, s := range m.ordered() {
		if m.idleEligibleLocked(s) {
			candidates = append(candidates, s)
		}
	}
	m.mu.Unlock()
	for _, s := range candidates {
		callCtx, cancel := context.WithTimeout(ctx, stopTimeout)
		err := m.stopServiceIfEligible(callCtx, s, true, nil)
		cancel()
		if err != nil {
			m.log.Warn("idle stop failed", "error", err)
		}
	}
}

func (m *Manager) finishRefresh() {
	m.mu.Lock()
	m.refreshing = false
	pending := m.deferredStops
	m.deferredStops = nil
	m.mu.Unlock()
	for s := range pending {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		err := m.stopServiceIfEligible(ctx, s, true, nil)
		cancel()
		if err != nil {
			m.log.Warn("released requirement stop failed", "error", err)
		}
	}
}
