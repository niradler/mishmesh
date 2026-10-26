package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLockServiceMutationHonorsContext(t *testing.T) {
	api, _ := newTestAPI(t)
	if err := api.lockServiceMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer api.unlockServiceMutation()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := api.lockServiceMutation(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}

	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := api.lockServiceMutation(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestLockServiceMutationIsExclusive(t *testing.T) {
	api, _ := newTestAPI(t)
	if err := api.lockServiceMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		if err := api.lockServiceMutation(context.Background()); err == nil {
			close(acquired)
			api.unlockServiceMutation()
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second holder acquired the lock while it was held")
	case <-time.After(50 * time.Millisecond):
	}
	api.unlockServiceMutation()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("waiter never acquired the released lock")
	}
}

func BenchmarkLockServiceMutationHandoff(b *testing.B) {
	api := New(nil, nil, "", nil)
	ctx := context.Background()
	var total time.Duration
	for i := 0; i < b.N; i++ {
		if err := api.lockServiceMutation(ctx); err != nil {
			b.Fatal(err)
		}
		released := make(chan time.Time, 1)
		acquired := make(chan time.Time, 1)
		go func() {
			if err := api.lockServiceMutation(ctx); err == nil {
				acquired <- time.Now()
				api.unlockServiceMutation()
			}
		}()
		time.Sleep(200 * time.Microsecond)
		released <- time.Now()
		api.unlockServiceMutation()
		total += (<-acquired).Sub(<-released)
	}
	b.ReportMetric(float64(total.Nanoseconds())/float64(b.N), "handoff-ns/op")
}
