package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	usageFlushInterval = time.Second
	usageCacheTTL      = time.Second
	usageRedisTimeout  = time.Second
	usageWarnInterval  = 10 * time.Second
	usageKeyPrefix     = "mm:usage:"
)

type orgUsage struct {
	local   atomic.Int64
	pending atomic.Int64

	mu        sync.Mutex
	remote    int64
	fetchedAt time.Time
	fetched   bool
}

type usageTracker struct {
	rdb           *goredis.Client
	flushInterval time.Duration
	cacheTTL      time.Duration
	now           func() time.Time

	orgs sync.Map

	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
	started   atomic.Bool

	warnMu         sync.Mutex
	warnAt         time.Time
	warnSuppressed int
}

func newUsageTracker(rdb *goredis.Client) *usageTracker {
	return &usageTracker{
		rdb:           rdb,
		flushInterval: usageFlushInterval,
		cacheTTL:      usageCacheTTL,
		now:           time.Now,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
}

func (u *usageTracker) org(orgID string) *orgUsage {
	if v, ok := u.orgs.Load(orgID); ok {
		return v.(*orgUsage)
	}
	v, _ := u.orgs.LoadOrStore(orgID, new(orgUsage))
	return v.(*orgUsage)
}

func (u *usageTracker) add(orgID string, bytes int64) {
	if bytes == 0 {
		return
	}
	o := u.org(orgID)
	o.local.Add(bytes)
	o.pending.Add(bytes)
	u.startOnce.Do(func() {
		u.started.Store(true)
		go u.loop()
	})
}

func (u *usageTracker) get(orgID string) int64 {
	o := u.org(orgID)
	o.mu.Lock()
	if o.fetched && u.now().Sub(o.fetchedAt) < u.cacheTTL {
		v := o.remote + o.pending.Load()
		o.mu.Unlock()
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageRedisTimeout)
	val, err := u.rdb.Get(ctx, usageKeyPrefix+orgID).Int64()
	cancel()
	switch {
	case err == nil:
		o.remote = val
	case errors.Is(err, goredis.Nil):
		o.remote = 0
	default:
		u.warn("redis get usage failed, using local counter", "org_id", orgID, "err", err)
		o.remote = o.local.Load() - o.pending.Load()
	}
	o.fetched = true
	o.fetchedAt = u.now()
	v := o.remote + o.pending.Load()
	o.mu.Unlock()
	return v
}

func (u *usageTracker) loop() {
	defer close(u.done)
	t := time.NewTicker(u.flushInterval)
	defer t.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-t.C:
			u.flush(usageRedisTimeout)
		}
	}
}

type flushItem struct {
	org *orgUsage
	id  string
	n   int64
	cmd *goredis.IntCmd
}

func (u *usageTracker) flush(timeout time.Duration) {
	var items []flushItem
	u.orgs.Range(func(k, v any) bool {
		o := v.(*orgUsage)
		if n := o.pending.Swap(0); n != 0 {
			items = append(items, flushItem{org: o, id: k.(string), n: n})
		}
		return true
	})
	if len(items) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	pipe := u.rdb.Pipeline()
	for i := range items {
		items[i].cmd = pipe.IncrBy(ctx, usageKeyPrefix+items[i].id, items[i].n)
	}
	_, _ = pipe.Exec(ctx)
	for _, it := range items {
		if err := it.cmd.Err(); err != nil {
			it.org.pending.Add(it.n)
			u.warn("redis incrby usage failed, will retry", "org_id", it.id, "err", err)
			continue
		}
		it.org.mu.Lock()
		if it.org.fetched {
			it.org.remote += it.n
		}
		it.org.mu.Unlock()
	}
}

func (u *usageTracker) close() {
	u.stopOnce.Do(func() {
		close(u.stop)
		if u.started.Load() {
			<-u.done
		}
		u.flush(2 * time.Second)
	})
}

func (u *usageTracker) warn(msg string, args ...any) {
	u.warnMu.Lock()
	now := u.now()
	if !u.warnAt.IsZero() && now.Sub(u.warnAt) < usageWarnInterval {
		u.warnSuppressed++
		u.warnMu.Unlock()
		return
	}
	suppressed := u.warnSuppressed
	u.warnSuppressed = 0
	u.warnAt = now
	u.warnMu.Unlock()
	if suppressed > 0 {
		args = append(args, "suppressed", fmt.Sprint(suppressed))
	}
	slog.Warn(msg, args...)
}
