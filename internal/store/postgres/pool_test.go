package postgres

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPoolConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   PoolConfig
		want PoolConfig
	}{
		{"zero value", PoolConfig{}, PoolConfig{DefaultMaxOpenConns, DefaultMaxOpenConns, DefaultConnMaxLifetime, DefaultConnMaxIdleTime, DefaultQueryTimeout}},
		{"idle clamped to max", PoolConfig{MaxOpenConns: 5, MaxIdleConns: 50}, PoolConfig{5, 5, DefaultConnMaxLifetime, DefaultConnMaxIdleTime, DefaultQueryTimeout}},
		{"explicit", PoolConfig{10, 2, time.Minute, time.Second, time.Second}, PoolConfig{10, 2, time.Minute, time.Second, time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.withDefaults(); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPoolBoundsOpenConnections(t *testing.T) {
	s := newStoreWithPool(t, PoolConfig{MaxOpenConns: 3})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.db.ExecContext(ctx, `SELECT pg_sleep(0.02)`)
		}()
	}
	wg.Wait()
	stats := s.db.Stats()
	if stats.OpenConnections > 3 {
		t.Fatalf("open connections = %d, want <= 3", stats.OpenConnections)
	}
	if stats.MaxOpenConnections != 3 {
		t.Fatalf("MaxOpenConnections = %d, want 3", stats.MaxOpenConnections)
	}
}
