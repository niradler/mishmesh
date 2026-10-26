package services

import (
	"context"
	"testing"
	"time"
)

func TestAcquireDoesNotBlockIndefinitelyBehindReservation(t *testing.T) {
	def := Definition{ID: "svc", OrgID: "org", Driver: "fake"}
	m := newManager(Options{}, func(Definition) (Driver, error) { return &fakeDriver{state: Running}, nil })
	m.admissionWait = 50 * time.Millisecond
	m.Apply([]Definition{def})
	entered, release := make(chan struct{}), make(chan struct{})
	reserved := make(chan error, 1)
	go func() {
		reserved <- m.Reserve(context.Background(), []Definition{def}, func(LifecycleReservation) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	acquired := make(chan struct{})
	go func() {
		m.Acquire(def.ID)()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		close(release)
		<-reserved
		t.Fatal("Acquire blocked behind a long reservation")
	}
	close(release)
	if err := <-reserved; err != nil {
		t.Fatal(err)
	}
}
