package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	sessionKeyPrefix          = "mm:tsess:"
	subjectKeyPrefix          = "mm:tsubj:"
	endpointSessionsKeyPrefix = "mm:tep:"
	onceKeyPrefix             = "mm:once:"
)

type SessionStore struct {
	rdb *goredis.Client
}

var _ store.SessionStore = (*SessionStore)(nil)

func (c *ConnStore) Sessions() store.SessionStore { return &SessionStore{rdb: c.rdb} }

func opCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 2*redisOpTimeout)
}

func (s *SessionStore) PutTokenSession(ctx context.Context, ts *store.TokenSession) error {
	ttl := time.Until(ts.ExpiresAt)
	if ttl <= 0 {
		return errors.New("redis: token session already expired")
	}
	raw, err := json.Marshal(ts)
	if err != nil {
		return fmt.Errorf("redis: encode token session: %w", err)
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	indexes := sessionIndexes(ts)
	ttls := make([]*goredis.DurationCmd, len(indexes))
	read := s.rdb.Pipeline()
	for i, index := range indexes {
		ttls[i] = read.PTTL(ctx, index)
	}
	if _, err := read.Exec(ctx); err != nil {
		return fmt.Errorf("redis: index ttl: %w", err)
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, sessionKeyPrefix+ts.ID, raw, ttl)
	for i, index := range indexes {
		pipe.SAdd(ctx, index, ts.ID)
		if ttls[i].Val() < ttl {
			pipe.PExpire(ctx, index, ttl)
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis: put token session: %w", err)
	}
	return nil
}

func (s *SessionStore) RotateTokenSession(ctx context.Context, ts *store.TokenSession, prevTokenID string) error {
	ttl := time.Until(ts.ExpiresAt)
	if ttl <= 0 {
		return errors.New("redis: token session already expired")
	}
	raw, err := json.Marshal(ts)
	if err != nil {
		return fmt.Errorf("redis: encode token session: %w", err)
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	key := sessionKeyPrefix + ts.ID
	indexes := sessionIndexes(ts)
	err = s.rdb.Watch(ctx, func(tx *goredis.Tx) error {
		cur, err := tx.Get(ctx, key).Bytes()
		if errors.Is(err, goredis.Nil) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		var current store.TokenSession
		if err := json.Unmarshal(cur, &current); err != nil {
			return err
		}
		if current.TokenID != prevTokenID || !time.Now().Before(current.ExpiresAt) {
			return store.ErrNotFound
		}
		ttls := make([]time.Duration, len(indexes))
		for i, index := range indexes {
			if ttls[i], err = tx.PTTL(ctx, index).Result(); err != nil {
				return err
			}
		}
		_, err = tx.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
			pipe.Set(ctx, key, raw, ttl)
			for i, index := range indexes {
				if ttls[i] < ttl {
					pipe.PExpire(ctx, index, ttl)
				}
			}
			return nil
		})
		return err
	}, key)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, goredis.TxFailedErr):
		return store.ErrNotFound
	case err != nil:
		return fmt.Errorf("redis: rotate token session: %w", err)
	}
	return nil
}

func (s *SessionStore) GetTokenSession(ctx context.Context, id string) (*store.TokenSession, error) {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	raw, err := s.rdb.Get(ctx, sessionKeyPrefix+id).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("redis: get token session: %w", err)
	}
	var ts store.TokenSession
	if err := json.Unmarshal(raw, &ts); err != nil {
		return nil, fmt.Errorf("redis: decode token session: %w", err)
	}
	if !time.Now().Before(ts.ExpiresAt) {
		return nil, store.ErrNotFound
	}
	return &ts, nil
}

func (s *SessionStore) DeleteTokenSession(ctx context.Context, id string) error {
	ts, err := s.GetTokenSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, sessionKeyPrefix+id)
	for _, index := range sessionIndexes(ts) {
		pipe.SRem(ctx, index, id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis: delete token session: %w", err)
	}
	return nil
}

func (s *SessionStore) DeleteSubjectSessions(ctx context.Context, orgID, subjectType, subjectID string) ([]string, error) {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	subject := subjectKeyPrefix + store.SubjectKey(orgID, subjectType, subjectID)
	ids, err := s.rdb.SMembers(ctx, subject).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: list subject sessions: %w", err)
	}
	keys := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		keys = append(keys, sessionKeyPrefix+id)
	}
	keys = append(keys, subject)
	if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
		return nil, fmt.Errorf("redis: delete subject sessions: %w", err)
	}
	return ids, nil
}

func (s *SessionStore) ListEndpointSessions(ctx context.Context, orgID, endpointID string) ([]store.TokenSession, error) {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	return s.endpointSessions(ctx, orgID, endpointID)
}

func (s *SessionStore) DeleteEndpointSessions(ctx context.Context, orgID, endpointID string) ([]store.TokenSession, error) {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	live, err := s.endpointSessions(ctx, orgID, endpointID)
	if err != nil {
		return nil, err
	}
	pipe := s.rdb.TxPipeline()
	for _, ts := range live {
		pipe.Del(ctx, sessionKeyPrefix+ts.ID)
		pipe.SRem(ctx, subjectKeyPrefix+store.SubjectKey(ts.OrgID, ts.SubjectType, ts.SubjectID), ts.ID)
	}
	pipe.Del(ctx, endpointSessionsKeyPrefix+store.EndpointKey(orgID, endpointID))
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("redis: delete endpoint sessions: %w", err)
	}
	return live, nil
}

func (s *SessionStore) endpointSessions(ctx context.Context, orgID, endpointID string) ([]store.TokenSession, error) {
	index := endpointSessionsKeyPrefix + store.EndpointKey(orgID, endpointID)
	ids, err := s.rdb.SMembers(ctx, index).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: list endpoint sessions: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = sessionKeyPrefix + id
	}
	raws, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: get endpoint sessions: %w", err)
	}
	now := time.Now()
	var live []store.TokenSession
	var stale []any
	for i, raw := range raws {
		var ts store.TokenSession
		str, ok := raw.(string)
		if !ok || json.Unmarshal([]byte(str), &ts) != nil || ts.OrgID != orgID || ts.EndpointID != endpointID || !now.Before(ts.ExpiresAt) {
			stale = append(stale, ids[i])
			continue
		}
		live = append(live, ts)
	}
	if len(stale) > 0 {
		if err := s.rdb.SRem(ctx, index, stale...).Err(); err != nil {
			return nil, fmt.Errorf("redis: prune endpoint sessions: %w", err)
		}
	}
	slices.SortFunc(live, func(a, b store.TokenSession) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return live, nil
}

func sessionIndexes(ts *store.TokenSession) []string {
	indexes := []string{subjectKeyPrefix + store.SubjectKey(ts.OrgID, ts.SubjectType, ts.SubjectID)}
	if ts.EndpointID != "" {
		indexes = append(indexes, endpointSessionsKeyPrefix+store.EndpointKey(ts.OrgID, ts.EndpointID))
	}
	return indexes
}

func (s *SessionStore) ClaimOnce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	ok, err := s.rdb.SetNX(ctx, onceKeyPrefix+key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis: claim once: %w", err)
	}
	return ok, nil
}

func (s *SessionStore) PutOnce(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	if err := s.rdb.Set(ctx, onceKeyPrefix+key, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis: put once: %w", err)
	}
	return nil
}

func (s *SessionStore) TakeOnce(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	raw, err := s.rdb.GetDel(ctx, onceKeyPrefix+key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("redis: take once: %w", err)
	}
	return raw, nil
}

func (s *SessionStore) PeekOnce(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	raw, err := s.rdb.Get(ctx, onceKeyPrefix+key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("redis: peek once: %w", err)
	}
	return raw, nil
}
