package ratelimit

import (
	"context"
	"time"
)

type Limit struct {
	Requests int
	Period   time.Duration
	Burst    int
}

func (l Limit) Capacity() int {
	if l.Burst > 0 {
		return l.Burst
	}
	return l.Requests
}

func (l Limit) Valid() bool {
	return l.Requests > 0 && l.Period > 0
}

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type Limiter interface {
	Allow(ctx context.Context, key string, limit Limit) Decision
}

func RetryAfterSeconds(d time.Duration) int {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		return 1
	}
	return secs
}
