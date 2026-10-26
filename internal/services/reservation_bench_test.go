package services

import (
	"context"
	"testing"
	"time"
)

func BenchmarkReserveHandoff(b *testing.B) {
	def := Definition{ID: "svc", OrgID: "org", Driver: "fake"}
	m := newManager(Options{}, func(Definition) (Driver, error) { return &fakeDriver{state: Stopped}, nil })
	m.Apply([]Definition{def})
	ctx := context.Background()
	var total time.Duration
	for b.Loop() {
		holding, release := make(chan struct{}), make(chan struct{})
		first := make(chan error, 1)
		go func() {
			first <- m.Reserve(ctx, []Definition{def}, func(LifecycleReservation) error {
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding
		started := make(chan time.Time, 1)
		second := make(chan error, 1)
		go func() {
			second <- m.Reserve(ctx, []Definition{def}, func(LifecycleReservation) error {
				started <- time.Now()
				return nil
			})
		}()
		time.Sleep(5 * time.Millisecond)
		released := time.Now()
		close(release)
		<-first
		total += (<-started).Sub(released)
		<-second
	}
	b.ReportMetric(float64(total.Microseconds())/float64(b.N), "handoff-µs/op")
}
