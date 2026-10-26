package accesslog

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	DefaultRetention     = 168 * time.Hour
	defaultQueueSize     = 4096
	defaultBatchSize     = 256
	defaultFlushInterval = time.Second
	defaultPruneInterval = time.Hour
	defaultFailureRate   = 50
	defaultFailureBurst  = 100
	defaultPerKeyWindow  = 10 * time.Second
	maxFailureKeys       = 10000
	writeTimeout         = 10 * time.Second
)

type Sink interface {
	AppendAccessEvents(ctx context.Context, events []*store.AccessEvent) error
	PruneAccessEvents(ctx context.Context, before time.Time) (int64, error)
}

type Options struct {
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	Retention     time.Duration
	PruneInterval time.Duration
	FailureRate   float64
	FailureBurst  float64
	PerKeyWindow  time.Duration
	Log           *slog.Logger
	Now           func() time.Time
}

type Logger struct {
	sink  Sink
	opts  Options
	queue chan *store.AccessEvent

	dropped    atomic.Int64
	suppressed atomic.Int64
	failed     atomic.Int64

	mu       sync.Mutex
	tokens   float64
	refillAt time.Time
	lastSeen map[string]time.Time
	purgedAt time.Time
}

func New(sink Sink, opts Options) *Logger {
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = defaultFlushInterval
	}
	if opts.Retention <= 0 {
		opts.Retention = DefaultRetention
	}
	if opts.PruneInterval <= 0 {
		opts.PruneInterval = defaultPruneInterval
	}
	if opts.FailureRate <= 0 {
		opts.FailureRate = defaultFailureRate
	}
	if opts.FailureBurst <= 0 {
		opts.FailureBurst = defaultFailureBurst
	}
	if opts.PerKeyWindow <= 0 {
		opts.PerKeyWindow = defaultPerKeyWindow
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Logger{
		sink:     sink,
		opts:     opts,
		queue:    make(chan *store.AccessEvent, opts.QueueSize),
		tokens:   opts.FailureBurst,
		refillAt: opts.Now(),
		lastSeen: map[string]time.Time{},
	}
}

func (l *Logger) Record(e *store.AccessEvent) {
	if l == nil || e == nil || e.OrgID == "" {
		return
	}
	if e.ID == "" {
		e.ID = store.NewID("acc")
	}
	if e.At.IsZero() {
		e.At = l.opts.Now().UTC()
	}
	if IsAuthFailure(e) && !l.admitFailure(failureKey(e)) {
		l.suppressed.Add(1)
		return
	}
	select {
	case l.queue <- e:
	default:
		l.dropped.Add(1)
	}
}

func (l *Logger) Dropped() int64    { return l.dropped.Load() }
func (l *Logger) Suppressed() int64 { return l.suppressed.Load() }
func (l *Logger) WriteFailures() int64 {
	return l.failed.Load()
}

func IsAuthFailure(e *store.AccessEvent) bool {
	return e.Decision == store.AccessDeny && (e.PrincipalID == "" || e.Status == 401)
}

func failureKey(e *store.AccessEvent) string {
	if e.PrincipalID != "" {
		return e.PrincipalType + ":" + e.PrincipalID + "|" + e.Reason
	}
	return "ip:" + e.ClientIP + "|" + e.Reason
}

func (l *Logger) admitFailure(key string) bool {
	now := l.opts.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tokens = min(l.opts.FailureBurst, l.tokens+now.Sub(l.refillAt).Seconds()*l.opts.FailureRate)
	l.refillAt = now
	if last, ok := l.lastSeen[key]; ok && now.Sub(last) < l.opts.PerKeyWindow {
		return false
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	if len(l.lastSeen) >= maxFailureKeys && now.Sub(l.purgedAt) >= l.opts.PerKeyWindow {
		l.dropExpiredKeys(now)
	}
	if len(l.lastSeen) < maxFailureKeys {
		l.lastSeen[key] = now
	}
	return true
}

func (l *Logger) dropExpiredKeys(now time.Time) {
	l.purgedAt = now
	for k, last := range l.lastSeen {
		if now.Sub(last) >= l.opts.PerKeyWindow {
			delete(l.lastSeen, k)
		}
	}
}

func (l *Logger) Run(ctx context.Context) {
	flush := time.NewTicker(l.opts.FlushInterval)
	defer flush.Stop()
	prune := time.NewTicker(l.opts.PruneInterval)
	defer prune.Stop()
	l.prune()
	batch := make([]*store.AccessEvent, 0, l.opts.BatchSize)
	write := func() {
		if len(batch) == 0 {
			return
		}
		l.write(batch)
		batch = make([]*store.AccessEvent, 0, l.opts.BatchSize)
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case e := <-l.queue:
					batch = append(batch, e)
					if len(batch) >= l.opts.BatchSize {
						write()
					}
				default:
					write()
					return
				}
			}
		case e := <-l.queue:
			batch = append(batch, e)
			if len(batch) >= l.opts.BatchSize {
				write()
			}
		case <-flush.C:
			write()
		case <-prune.C:
			l.prune()
		}
	}
}

func (l *Logger) write(batch []*store.AccessEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := l.sink.AppendAccessEvents(ctx, batch); err != nil {
		l.failed.Add(int64(len(batch)))
		l.opts.Log.Error("access log write failed", "events", len(batch), "err", err)
	}
}

func (l *Logger) prune() {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	n, err := l.sink.PruneAccessEvents(ctx, l.opts.Now().Add(-l.opts.Retention))
	if err != nil {
		l.opts.Log.Error("access log prune failed", "err", err)
		return
	}
	if n > 0 {
		l.opts.Log.Info("access log pruned", "events", n)
	}
}
