package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/store"
)

type blockingHealth struct {
	started chan struct{}
	done    chan struct{}
}

func (b *blockingHealth) Get(string) (health.EndpointHealth, bool) {
	return health.EndpointHealth{}, false
}
func (b *blockingHealth) Forget(string) {}
func (b *blockingHealth) ProbeNow(ctx context.Context, _ string) (health.EndpointHealth, error) {
	close(b.started)
	<-ctx.Done()
	close(b.done)
	return health.EndpointHealth{}, ctx.Err()
}

func TestCloseStopsEndpointHealthProbe(t *testing.T) {
	api, _ := newTestAPI(t)
	probe := &blockingHealth{started: make(chan struct{}), done: make(chan struct{})}
	api.SetHealth(probe)
	api.endpointChanged(&store.Endpoint{ID: "ep_1", OrgID: defaultOrgID})
	select {
	case <-probe.started:
	case <-time.After(2 * time.Second):
		t.Fatal("probe never started")
	}
	closed := make(chan struct{})
	go func() {
		api.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not stop the in-flight probe")
	}
	select {
	case <-probe.done:
	default:
		t.Fatal("probe goroutine still running after Close")
	}
}
