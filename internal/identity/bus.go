package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	revokeTopic       = "revoke"
	flushTopic        = "flush"
	busPublishTimeout = 2 * time.Second
)

type busMessage struct {
	Origin      string   `json:"origin"`
	OrgID       string   `json:"org,omitempty"`
	SubjectType string   `json:"subject_type,omitempty"`
	SubjectID   string   `json:"subject_id,omitempty"`
	SessionIDs  []string `json:"sessions,omitempty"`
}

func (s *Service) AttachBus(bus store.EventBus, log *slog.Logger) (func(), error) {
	if log == nil {
		log = slog.Default()
	}
	stopRevoke, err := bus.SubscribeEvents(revokeTopic, s.handleRemoteRevoke)
	if err != nil {
		return nil, fmt.Errorf("identity: subscribe revocations: %w", err)
	}
	stopFlush, err := bus.SubscribeEvents(flushTopic, s.handleRemoteFlush)
	if err != nil {
		stopRevoke()
		return nil, fmt.Errorf("identity: subscribe flushes: %w", err)
	}
	s.mu.Lock()
	s.bus = bus
	s.busLog = log
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.bus = nil
		s.mu.Unlock()
		stopRevoke()
		stopFlush()
	}, nil
}

func (s *Service) OnFlush(fn func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	s.flushers[id] = fn
	return func() {
		s.mu.Lock()
		delete(s.flushers, id)
		s.mu.Unlock()
	}
}

func (s *Service) FlushAll() {
	s.flushLocal()
	s.broadcast(flushTopic, busMessage{Origin: s.origin})
}

func (s *Service) SessionActive(ctx context.Context, id string) (bool, error) {
	ts, err := s.sessions.GetTokenSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return s.now().Before(ts.ExpiresAt), nil
}

func (s *Service) flushLocal() {
	s.mu.Lock()
	fns := make([]func(), 0, len(s.flushers))
	for _, id := range slices.Sorted(maps.Keys(s.flushers)) {
		fns = append(fns, s.flushers[id])
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (s *Service) broadcastRevocation(rev Revocation) {
	subjectType, _ := SubjectType(rev.Subject.Type)
	s.broadcast(revokeTopic, busMessage{Origin: s.origin, OrgID: rev.OrgID, SubjectType: subjectType, SubjectID: rev.Subject.ID, SessionIDs: rev.SessionIDs})
}

func (s *Service) broadcast(topic string, msg busMessage) {
	s.mu.Lock()
	bus, log := s.bus, s.busLog
	s.mu.Unlock()
	if bus == nil {
		return
	}
	body, err := json.Marshal(msg)
	if err != nil {
		log.Error("encode cluster event failed", "topic", topic, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	if err := bus.PublishEvent(ctx, topic, body); err != nil {
		log.Error("publish cluster event failed; other nodes rely on the session sweep", "topic", topic, "err", err)
	}
}

func (s *Service) decodeRemote(payload []byte) (busMessage, bool) {
	var msg busMessage
	if json.Unmarshal(payload, &msg) != nil || msg.Origin == "" || msg.Origin == s.origin {
		return busMessage{}, false
	}
	return msg, true
}

func (s *Service) handleRemoteRevoke(payload []byte) {
	msg, ok := s.decodeRemote(payload)
	if !ok || msg.OrgID == "" || len(msg.SessionIDs) == 0 {
		return
	}
	entity, _ := EntityType(msg.SubjectType)
	s.deliver(Revocation{OrgID: msg.OrgID, Subject: authz.EntityRef{Type: entity, ID: msg.SubjectID}, SessionIDs: msg.SessionIDs})
}

func (s *Service) handleRemoteFlush(payload []byte) {
	if _, ok := s.decodeRemote(payload); ok {
		s.flushLocal()
	}
}
