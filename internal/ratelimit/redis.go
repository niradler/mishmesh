package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	redisOpTimeout = 200 * time.Millisecond
	warnInterval   = 10 * time.Second
	keyPrefix      = "mm:rl:"
)

var errUnexpectedReply = errors.New("ratelimit: unexpected script reply")

var bucketScript = goredis.NewScript(`
local capacity = tonumber(ARGV[1])
local per_token_ms = tonumber(ARGV[2])
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local state = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(state[1])
local ts = tonumber(state[2])
if tokens == nil or ts == nil then
  tokens = capacity
  ts = now
end
local elapsed = now - ts
if elapsed < 0 then elapsed = 0 end
tokens = math.min(capacity, tokens + elapsed / per_token_ms)
local allowed = 0
local retry = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry = math.ceil((1 - tokens) * per_token_ms)
end
redis.call('HSET', KEYS[1], 'tokens', string.format('%.6f', tokens), 'ts', now)
redis.call('PEXPIRE', KEYS[1], math.ceil(capacity * per_token_ms) + 1000)
return {allowed, retry}
`)

type Redis struct {
	rdb      *goredis.Client
	log      *slog.Logger
	lastWarn atomic.Int64
}

var _ Limiter = (*Redis)(nil)

func NewRedis(rdb *goredis.Client, log *slog.Logger) *Redis {
	if log == nil {
		log = slog.Default()
	}
	return &Redis{rdb: rdb, log: log}
}

func (r *Redis) Allow(ctx context.Context, key string, limit Limit) Decision {
	if !limit.Valid() {
		return Decision{Allowed: true}
	}
	perTokenMs := float64(limit.Period.Milliseconds()) / float64(limit.Requests)
	if perTokenMs < 1 {
		perTokenMs = 1
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	res, err := bucketScript.Run(ctx, r.rdb, []string{keyPrefix + key}, limit.Capacity(), perTokenMs).Int64Slice()
	if err == nil && len(res) != 2 {
		err = errUnexpectedReply
	}
	if err != nil {
		r.warn(err)
		return Decision{Allowed: true}
	}
	if res[0] == 1 {
		return Decision{Allowed: true}
	}
	return Decision{RetryAfter: time.Duration(res[1]) * time.Millisecond}
}

func (r *Redis) warn(err error) {
	now := time.Now().UnixNano()
	last := r.lastWarn.Load()
	if now-last < int64(warnInterval) || !r.lastWarn.CompareAndSwap(last, now) {
		return
	}
	r.log.Warn("rate limiter redis error, failing open", "err", err)
}
