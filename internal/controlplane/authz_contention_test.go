package controlplane

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type gatedPolicyStore struct {
	store.DataStore
	gateOrg string
	entered chan struct{}
	release chan struct{}
	loads   atomic.Int32
}

func (g *gatedPolicyStore) GetOrgPolicy(ctx context.Context, orgID string) (*store.OrgPolicy, error) {
	g.loads.Add(1)
	pol, err := g.DataStore.GetOrgPolicy(ctx, orgID)
	if orgID == g.gateOrg {
		select {
		case g.entered <- struct{}{}:
		default:
		}
		<-g.release
	}
	return pol, err
}

func newGatedAPI(t *testing.T, gateOrg string) (*API, *gatedPolicyStore) {
	t.Helper()
	inner, err := sqlite.Open(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	g := &gatedPolicyStore{DataStore: inner, gateOrg: gateOrg, entered: make(chan struct{}, 8), release: make(chan struct{})}
	api := New(g, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	return api, g
}

func TestSlowOrgPolicyLoadDoesNotBlockOtherOrgs(t *testing.T) {
	api, g := newGatedAPI(t, "org_slow")
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(g.release) }) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = api.orgAuthorizer(context.Background(), "org_slow")
	}()
	<-g.entered

	fast := make(chan struct{})
	go func() {
		defer close(fast)
		_, _ = api.orgAuthorizer(context.Background(), "org_fast")
	}()
	select {
	case <-fast:
	case <-time.After(2 * time.Second):
		t.Fatal("org_fast blocked behind org_slow's policy load")
	}
	once.Do(func() { close(g.release) })
	<-done
}

func TestInvalidateDuringLoadDropsStaleResult(t *testing.T) {
	api, g := newGatedAPI(t, "org_x")
	ctx := context.Background()
	if err := api.data.CreateOrg(ctx, &store.Org{ID: "org_x", Name: "x", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	loaded := make(chan *authz.Authorizer, 1)
	go func() {
		az, _ := api.orgAuthorizer(ctx, "org_x")
		loaded <- az
	}()
	<-g.entered

	src := authz.CompileMatrix(map[string][]authz.Action{"owner": {"agent:read"}})
	if err := api.data.SetOrgPolicy(ctx, &store.OrgPolicy{OrgID: "org_x", CedarSrc: src, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	invalidated := make(chan struct{})
	go func() {
		api.invalidateAuthz("org_x")
		close(invalidated)
	}()
	select {
	case <-invalidated:
	case <-time.After(2 * time.Second):
		t.Fatal("invalidateAuthz blocked behind an in-flight policy load")
	}
	close(g.release)
	stale := <-loaded

	fresh, err := api.orgAuthorizer(ctx, "org_x")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == stale || fresh == api.defaultAuthz {
		t.Fatal("authorizer loaded before the invalidation was cached and served after it")
	}
}

func TestConcurrentMissesShareOnePolicyLoad(t *testing.T) {
	api, g := newGatedAPI(t, "org_x")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = api.orgAuthorizer(context.Background(), "org_x")
		}()
	}
	<-g.entered
	time.Sleep(100 * time.Millisecond)
	close(g.release)
	wg.Wait()
	if n := g.loads.Load(); n != 1 {
		t.Fatalf("policy loaded %d times for 8 concurrent misses, want 1", n)
	}
}

func TestOrgAuthorizerHonorsContextCancellationWhileWaiting(t *testing.T) {
	api, g := newGatedAPI(t, "org_x")
	t.Cleanup(func() { close(g.release) })
	go func() { _, _ = api.orgAuthorizer(context.Background(), "org_x") }()
	<-g.entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := api.orgAuthorizer(ctx, "org_x"); err == nil {
		t.Fatal("waiter ignored its own cancelled context")
	}
}
