package memory

import (
	"context"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const sessionPruneEvery = 256

type onceValue struct {
	value     []byte
	expiresAt time.Time
}

type SessionStore struct {
	now func() time.Time

	mu        sync.Mutex
	sessions  map[string]store.TokenSession
	bySubject map[string]map[string]struct{}
	once      map[string]onceValue
	writes    int
}

var _ store.SessionStore = (*SessionStore)(nil)

func NewSessionStore() *SessionStore {
	return newSessionStore(time.Now)
}

func NewSessionStoreWithClock(now func() time.Time) *SessionStore {
	return newSessionStore(now)
}

func newSessionStore(now func() time.Time) *SessionStore {
	return &SessionStore{
		now:       now,
		sessions:  make(map[string]store.TokenSession),
		bySubject: make(map[string]map[string]struct{}),
		once:      make(map[string]onceValue),
	}
}

func (s *SessionStore) PutTokenSession(_ context.Context, ts *store.TokenSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.sessions[ts.ID] = *ts
	key := store.SubjectKey(ts.OrgID, ts.SubjectType, ts.SubjectID)
	set := s.bySubject[key]
	if set == nil {
		set = make(map[string]struct{})
		s.bySubject[key] = set
	}
	set[ts.ID] = struct{}{}
	return nil
}

func (s *SessionStore) GetTokenSession(_ context.Context, id string) (*store.TokenSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.sessions[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	if !s.now().Before(ts.ExpiresAt) {
		s.deleteLocked(id)
		return nil, store.ErrNotFound
	}
	return &ts, nil
}

func (s *SessionStore) DeleteTokenSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteLocked(id)
	return nil
}

func (s *SessionStore) DeleteSubjectSessions(_ context.Context, orgID, subjectType, subjectID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := store.SubjectKey(orgID, subjectType, subjectID)
	var ids []string
	for id := range s.bySubject[key] {
		ids = append(ids, id)
		delete(s.sessions, id)
	}
	delete(s.bySubject, key)
	return ids, nil
}

func (s *SessionStore) ClaimOnce(_ context.Context, key string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if v, ok := s.once[key]; ok && now.Before(v.expiresAt) {
		return false, nil
	}
	s.pruneLocked()
	s.once[key] = onceValue{expiresAt: now.Add(ttl)}
	return true, nil
}

func (s *SessionStore) PutOnce(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.once[key] = onceValue{value: append([]byte(nil), value...), expiresAt: s.now().Add(ttl)}
	return nil
}

func (s *SessionStore) TakeOnce(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.once[key]
	delete(s.once, key)
	if !ok || !s.now().Before(v.expiresAt) {
		return nil, store.ErrNotFound
	}
	return v.value, nil
}

func (s *SessionStore) PeekOnce(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.once[key]
	if !ok || !s.now().Before(v.expiresAt) || v.value == nil {
		return nil, store.ErrNotFound
	}
	return append([]byte(nil), v.value...), nil
}

func (s *SessionStore) deleteLocked(id string) {
	ts, ok := s.sessions[id]
	if !ok {
		return
	}
	delete(s.sessions, id)
	key := store.SubjectKey(ts.OrgID, ts.SubjectType, ts.SubjectID)
	if set := s.bySubject[key]; set != nil {
		delete(set, id)
		if len(set) == 0 {
			delete(s.bySubject, key)
		}
	}
}

func (s *SessionStore) pruneLocked() {
	s.writes++
	if s.writes%sessionPruneEvery != 0 {
		return
	}
	now := s.now()
	for id, ts := range s.sessions {
		if !now.Before(ts.ExpiresAt) {
			s.deleteLocked(id)
		}
	}
	for key, v := range s.once {
		if !now.Before(v.expiresAt) {
			delete(s.once, key)
		}
	}
}
