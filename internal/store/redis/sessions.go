package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	sessionKeyPrefix = "mm:tsess:"
	subjectKeyPrefix = "mm:tsubj:"
	onceKeyPrefix    = "mm:once:"
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
	subject := subjectKeyPrefix + store.SubjectKey(ts.OrgID, ts.SubjectType, ts.SubjectID)
	current, err := s.rdb.PTTL(ctx, subject).Result()
	if err != nil {
		return fmt.Errorf("redis: subject ttl: %w", err)
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, sessionKeyPrefix+ts.ID, raw, ttl)
	pipe.SAdd(ctx, subject, ts.ID)
	if current < ttl {
		pipe.PExpire(ctx, subject, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis: put token session: %w", err)
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
	pipe.SRem(ctx, subjectKeyPrefix+store.SubjectKey(ts.OrgID, ts.SubjectType, ts.SubjectID), id)
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
