package services

import (
	"context"
	"strconv"
	"testing"
	"time"
)

type slowStateDriver struct {
	fakeDriver
	delay time.Duration
}

func (d *slowStateDriver) State(ctx context.Context) (State, error) {
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return Unknown, ctx.Err()
	}
	return Running, nil
}

func TestRefreshProbesDriversConcurrently(t *testing.T) {
	const services = 8
	const delay = 150 * time.Millisecond
	defs := make([]Definition, services)
	for i := range defs {
		defs[i] = Definition{ID: "svc" + strconv.Itoa(i), OrgID: "org", Name: "svc" + strconv.Itoa(i), Driver: "fake"}
	}
	m := newManager(Options{}, func(Definition) (Driver, error) { return &slowStateDriver{delay: delay}, nil })
	m.Apply(defs)
	started := time.Now()
	m.Refresh(context.Background())
	if elapsed := time.Since(started); elapsed > 4*delay {
		t.Fatalf("refresh took %s for %d services; probes are serial", elapsed, services)
	}
	for _, def := range defs {
		status, ok := m.Status(def.ID)
		if !ok || status.State != StateRunning {
			t.Fatalf("%s state = %q", def.ID, status.State)
		}
	}
}
