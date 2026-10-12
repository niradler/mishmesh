package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeInstance struct {
	sync       func(context.Context) (Status, error)
	status     Status
	closed     atomic.Int32
	close      func()
	readStatus func() Status
	run        func(context.Context)
}

func (i *fakeInstance) Run(ctx context.Context) {
	if i.run != nil {
		i.run(ctx)
		return
	}
	<-ctx.Done()
}
func (i *fakeInstance) Sync(ctx context.Context) (Status, error) {
	if i.sync != nil {
		return i.sync(ctx)
	}
	return i.Status(), nil
}
func (i *fakeInstance) Status() Status {
	if i.readStatus != nil {
		return i.readStatus()
	}
	return i.status
}
func (i *fakeInstance) Close() {
	i.closed.Add(1)
	if i.close != nil {
		i.close()
	}
}

func connectionStore(t *testing.T, ids ...string) *sqlite.Store {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	now := time.Now()
	if err := data.CreateOrg(context.Background(), &store.Org{ID: "org", Name: "org", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := data.CreateConnection(context.Background(), &store.Connection{ID: id, OrgID: "org", Name: id, Type: "fake", Config: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

func testHub(t *testing.T, data store.DataStore, provider Provider, box *secrets.Box) *Hub {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(HubOptions{Registry: registry, Deps: Deps{Data: data, Box: box, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, SyncTimeout: time.Second, DrainTimeout: 40 * time.Millisecond})
	t.Cleanup(hub.Close)
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return hub
}

func TestShouldOwnIndependentSameOrgInstancesAndIsolateOpenFailure(t *testing.T) {
	data := connectionStore(t, "one", "two", "bad")
	instances := map[string]*fakeInstance{}
	provider := testProvider(func(cfg Config, deps Deps) (Instance, error) {
		instance := &fakeInstance{status: Status{Health: "healthy", Level: cfg.Record.ID}}
		instances[cfg.Record.ID] = instance
		if cfg.Record.ID == "bad" {
			return instance, errors.New("credential confidential")
		}
		return instance, nil
	})
	hub := testHub(t, data, provider, nil)
	for _, id := range []string{"one", "two"} {
		status, err := hub.Sync(context.Background(), id)
		if err != nil || status.Level != id {
			t.Fatalf("%s: %+v %v", id, status, err)
		}
	}
	if hub.Status("bad").Health != "down" || strings.Contains(hub.Status("bad").LastError, "confidential") {
		t.Fatal("failed open leaked or did not go down")
	}
	hub.Close()
	hub.Close()
	for id, instance := range instances {
		if instance.closed.Load() != 1 {
			t.Fatalf("%s closed %d times", id, instance.closed.Load())
		}
	}
}

type failedListStore struct{ store.DataStore }

func (failedListStore) ListAllConnections(context.Context) ([]*store.Connection, error) {
	return nil, errors.New("database password")
}

func TestShouldAbortStartupOnStoreListFailure(t *testing.T) {
	hub := NewHub(HubOptions{Registry: NewRegistry(), Deps: Deps{Data: failedListStore{}}})
	defer hub.Close()
	if err := hub.Start(context.Background()); err == nil || strings.Contains(err.Error(), "password") {
		t.Fatalf("error=%v", err)
	}
}

func TestShouldSerializeSyncAndCoalesceTriggers(t *testing.T) {
	data := connectionStore(t, "one")
	entered := make(chan struct{}, 20)
	release := make(chan struct{}, 20)
	var active, maximum, calls atomic.Int32
	instance := &fakeInstance{sync: func(ctx context.Context) (Status, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return Status{}, ctx.Err()
		}
		return Status{Health: "healthy"}, nil
	}}
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return instance, nil }), nil)
	hub.Trigger("one")
	waitSignal(t, entered)
	for i := 0; i < 100; i++ {
		hub.Trigger("one")
	}
	release <- struct{}{}
	waitSignal(t, entered)
	release <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatalf("triggers ran %d syncs", calls.Load())
	}
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, _ = hub.Sync(context.Background(), "one") }()
	}
	for i := 0; i < 8; i++ {
		waitSignal(t, entered)
		release <- struct{}{}
	}
	wait.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent sync=%d", maximum.Load())
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("signal deadline")
	}
}

func TestShouldCancelAndRetireOldGenerationDuringSync(t *testing.T) {
	data := connectionStore(t, "one")
	entered, release := make(chan struct{}), make(chan struct{})
	old := &fakeInstance{sync: func(context.Context) (Status, error) {
		close(entered)
		<-release
		return Status{Health: "healthy", Level: "old"}, nil
	}}
	newInstance := &fakeInstance{status: Status{Health: "healthy", Level: "new"}}
	var opens atomic.Int32
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) {
		if opens.Add(1) == 1 {
			return old, nil
		}
		return newInstance, nil
	}), nil)
	result := make(chan error, 1)
	go func() { _, err := hub.Sync(context.Background(), "one"); result <- err }()
	waitSignal(t, entered)
	if err := hub.Reload(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("old sync succeeded after retirement")
		}
	case <-time.After(time.Second):
		t.Fatal("old caller did not cancel")
	}
	if old.closed.Load() != 1 {
		t.Fatal("old instance not closed")
	}
	close(release)
	status, err := hub.Sync(context.Background(), "one")
	if err != nil || status.Level != "new" || hub.Status("one").Level != "new" {
		t.Fatalf("stale completion: %+v %v", status, err)
	}
	if err := data.DeleteConnection(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := hub.Reload(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if newInstance.closed.Load() != 1 || hub.Status("one").Health != "down" {
		t.Fatal("deletion did not retire")
	}
}

func TestShouldBoundCloseAndCanceledCallers(t *testing.T) {
	data := connectionStore(t, "one")
	entered, release, closeRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	instance := &fakeInstance{sync: func(context.Context) (Status, error) { close(entered); <-release; return Status{}, nil }, close: func() { <-closeRelease }}
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return instance, nil }), nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := hub.Sync(ctx, "one"); result <- err }()
	waitSignal(t, entered)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller did not cancel")
	}
	start := time.Now()
	hub.Close()
	hub.Close()
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("close exceeded bound")
	}
	close(release)
	close(closeRelease)
}

func TestShouldRejectBadSecretsWithoutAffectingOtherConnections(t *testing.T) {
	for _, mode := range []string{"missing-box", "wrong-box", "wrong-org", "wrong-id", "corrupt", "invalid-json", "duplicate-json", "null-json", "trailing-json"} {
		t.Run(mode, func(t *testing.T) {
			data := connectionStore(t, "bad", "good")
			box, _ := secrets.New(make([]byte, 32))
			sealContext := SecretContext("org", "bad")
			if mode == "wrong-org" {
				sealContext = SecretContext("foreign", "bad")
			}
			if mode == "wrong-id" {
				sealContext = SecretContext("org", "other")
			}
			plain := []byte(`{"token":"confidential"}`)
			if mode == "invalid-json" {
				plain = []byte(`{"token":4}`)
			}
			if mode == "duplicate-json" {
				plain = []byte(`{"token":"a","token":"b"}`)
			}
			if mode == "null-json" {
				plain = []byte(`{"token":null}`)
			}
			if mode == "trailing-json" {
				plain = []byte(`{"token":"a"} {}`)
			}
			sealed, _ := box.Seal(plain, sealContext)
			if mode == "corrupt" {
				sealed = []byte("corrupt confidential")
			}
			record, _ := data.GetConnection(context.Background(), "bad")
			record.SecretsEnc = sealed
			if err := data.UpdateConnection(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-box" {
				box = nil
			}
			if mode == "wrong-box" {
				key := make([]byte, 32)
				key[0] = 1
				box, _ = secrets.New(key)
			}
			var opens atomic.Int32
			hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) {
				opens.Add(1)
				return &fakeInstance{status: Status{Health: "healthy"}}, nil
			}), box)
			if opens.Load() != 1 || hub.Status("bad").Health != "down" || hub.Status("good").Health != "healthy" {
				t.Fatal("secret failure did not isolate record")
			}
		})
	}
}

func TestShouldRedactStatusesErrorsAndLogsAndCloneOwnership(t *testing.T) {
	data := connectionStore(t, "one")
	box, _ := secrets.New(make([]byte, 32))
	record, _ := data.GetConnection(context.Background(), "one")
	record.SecretsEnc, _ = box.Seal([]byte(`{"api_token":"confidential"}`), SecretContext("org", "one"))
	if err := data.UpdateConnection(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	status := Status{Health: "healthy", Level: "confidential", LastError: "token confidential", Hostnames: []HostStatus{{Host: "confidential", Detail: "confidential", Fix: "confidential"}}, Details: map[string]any{"nested": map[string]any{"message": "confidential"}, "api_token": "anything", "values": []string{"confidential"}}}
	var logs bytes.Buffer
	registry := NewRegistry()
	provider := testProvider(func(cfg Config, deps Deps) (Instance, error) {
		cfg.Secrets["api_token"] = "mutated"
		cfg.Record.Config[0] = 'x'
		deps.Log.Error("provider", "token", "confidential")
		deps.Log.With("nested", map[string]any{"token": "rotated", "safe": "confidential"}).WithGroup("group").Error("provider", "error", errors.New("transport confidential"))
		return &fakeInstance{status: status, sync: func(context.Context) (Status, error) { return status, errors.New("transport confidential") }}, nil
	})
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(HubOptions{Registry: registry, Deps: Deps{Data: data, Box: box, Log: slog.New(slog.NewTextHandler(&logs, nil))}, DrainTimeout: 40 * time.Millisecond})
	defer hub.Close()
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := hub.Sync(context.Background(), "one")
	raw, _ := json.Marshal(got)
	if err == nil || strings.Contains(err.Error(), "confidential") || bytes.Contains(raw, []byte("confidential")) || strings.Contains(logs.String(), "confidential") || strings.Contains(logs.String(), "rotated") {
		t.Fatalf("secret leaked: status=%s err=%v logs=%s", raw, err, logs.String())
	}
	got.Hostnames[0].Host = "caller"
	got.Details["nested"].(map[string]any)["message"] = "caller"
	if hub.Status("one").Hostnames[0].Host == "caller" || hub.Status("one").Details["nested"].(map[string]any)["message"] == "caller" {
		t.Fatal("status shares ownership")
	}
	stored, _ := data.GetConnection(context.Background(), "one")
	if string(stored.Config) != "{}" {
		t.Fatal("provider changed store config")
	}
}

func TestShouldLimitStalledRetiredGenerationsAndAllowOtherConnections(t *testing.T) {
	data := connectionStore(t, "one", "two")
	entered, release := make(chan struct{}), make(chan struct{})
	old := &fakeInstance{sync: func(context.Context) (Status, error) { close(entered); <-release; return Status{}, nil }}
	var opens atomic.Int32
	hub := testHub(t, data, testProvider(func(cfg Config, _ Deps) (Instance, error) {
		if cfg.Record.ID == "one" && opens.Add(1) == 1 {
			return old, nil
		}
		return &fakeInstance{status: Status{Health: "healthy"}}, nil
	}), nil)
	hub.Trigger("one")
	waitSignal(t, entered)
	if err := hub.Reload(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := hub.Reload(context.Background(), "one"); !errors.Is(err, ErrDrainPending) {
		t.Fatalf("err=%v", err)
	}
	if _, err := hub.Sync(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 2 {
		t.Fatalf("repeated reload opened %d generations", opens.Load())
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		err := hub.Reload(context.Background(), "one")
		if err == nil {
			break
		}
		if !errors.Is(err, ErrDrainPending) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestShouldCallProviderCallbacksOutsideHubLocks(t *testing.T) {
	data := connectionStore(t, "one")
	registry := NewRegistry()
	var hub *Hub
	var callbacks atomic.Int32
	instance := &fakeInstance{readStatus: func() Status { callbacks.Add(1); hub.Trigger("absent"); return Status{Health: "healthy"} }, sync: func(context.Context) (Status, error) {
		callbacks.Add(1)
		hub.Trigger("absent")
		return Status{Health: "healthy"}, nil
	}, close: func() { callbacks.Add(1); _ = hub.Status("one") }}
	provider := testProvider(func(Config, Deps) (Instance, error) { callbacks.Add(1); _ = hub.Status("absent"); return instance, nil })
	provider.validate = func(Config) error { callbacks.Add(1); _ = hub.Status("absent"); return nil }
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	hub = NewHub(HubOptions{Registry: registry, Deps: Deps{Data: data}, DrainTimeout: 40 * time.Millisecond})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = hub.Start(context.Background())
		_, _ = hub.Sync(context.Background(), "one")
		_ = hub.Status("one")
		_ = hub.Reload(context.Background(), "one")
		hub.Close()
	}()
	waitSignal(t, finished)
	if callbacks.Load() < 8 {
		t.Fatalf("callbacks=%d", callbacks.Load())
	}
}

func TestShouldBoundSyncWithoutSpawningCanceledWaiters(t *testing.T) {
	data := connectionStore(t, "one")
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	instance := &fakeInstance{sync: func(context.Context) (Status, error) { calls.Add(1); close(entered); <-release; return Status{}, nil }}
	registry := NewRegistry()
	if err := registry.Register(testProvider(func(Config, Deps) (Instance, error) { return instance, nil })); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(HubOptions{Registry: registry, Deps: Deps{Data: data}, SyncTimeout: 40 * time.Millisecond, DrainTimeout: 40 * time.Millisecond})
	defer hub.Close()
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := hub.Sync(context.Background(), "one"); finished <- err }()
	waitSignal(t, entered)
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := hub.Sync(ctx, "one"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sync deadline ignored")
	}
	if calls.Load() != 1 {
		t.Fatalf("canceled waiters invoked %d callbacks", calls.Load())
	}
	close(release)
}

func TestShouldKeepValidateAndOpenConfigOwnershipSeparate(t *testing.T) {
	data := connectionStore(t, "one")
	box, _ := secrets.New(make([]byte, 32))
	record, _ := data.GetConnection(context.Background(), "one")
	record.SecretsEnc, _ = box.Seal([]byte(`{"token":"secret"}`), SecretContext("org", "one"))
	if err := data.UpdateConnection(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var invalid atomic.Bool
	provider := testProvider(func(cfg Config, deps Deps) (Instance, error) {
		if cfg.Secrets["token"] != "secret" || string(cfg.Record.Config) != "{}" || len(cfg.Record.SecretsEnc) != 0 || !cfg.HostCommands || !deps.HostCommands {
			invalid.Store(true)
		}
		return &fakeInstance{status: Status{Health: "healthy"}}, nil
	})
	provider.validate = func(cfg Config) error { cfg.Secrets["token"] = "changed"; cfg.Record.Config[0] = 'x'; return nil }
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(HubOptions{Registry: registry, Deps: Deps{Data: data, Box: box, HostCommands: true}, DrainTimeout: 40 * time.Millisecond})
	defer hub.Close()
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if invalid.Load() {
		t.Fatal("config mutation or runtime host policy leaked across ownership")
	}
}

func TestShouldPreserveStableLoadDiagnostics(t *testing.T) {
	data := connectionStore(t, "one")
	hub := NewHub(HubOptions{Registry: NewRegistry(), Deps: Deps{Data: data, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}})
	defer hub.Close()
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := hub.Status("one"); got.Health != "down" || got.LastError != "unknown connection type" {
		t.Fatalf("status=%+v", got)
	}
}

func TestShouldReportStoppedParentAndRefuseReload(t *testing.T) {
	data := connectionStore(t, "one")
	registry := NewRegistry()
	if err := registry.Register(testProvider(func(Config, Deps) (Instance, error) { return &fakeInstance{status: Status{Health: "healthy"}}, nil })); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(HubOptions{Registry: registry, Deps: Deps{Data: data}, DrainTimeout: 40 * time.Millisecond})
	defer hub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	if err := hub.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if status := hub.Status("one"); status.Health != "down" {
		t.Fatalf("canceled parent status=%+v", status)
	}
	if err := hub.Reload(context.Background(), "one"); !errors.Is(err, ErrHubStopped) {
		t.Fatalf("err=%v", err)
	}
}

func TestShouldCloseLateOpenWithoutStartingGeneration(t *testing.T) {
	data := connectionStore(t, "one")
	entered, release := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	instance := &fakeInstance{status: Status{Health: "healthy"}, run: func(context.Context) { runs.Add(1) }}
	registry := NewRegistry()
	if err := registry.Register(testProvider(func(Config, Deps) (Instance, error) { close(entered); <-release; return instance, nil })); err != nil {
		t.Fatal(err)
	}
	hub := NewHub(HubOptions{Registry: registry, Deps: Deps{Data: data}, DrainTimeout: 40 * time.Millisecond})
	finished := make(chan error, 1)
	go func() { finished <- hub.Start(context.Background()) }()
	waitSignal(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := hub.Reload(ctx, "one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reload admission err=%v", err)
	}
	start := time.Now()
	hub.Close()
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("close blocked behind Open")
	}
	close(release)
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("startup succeeded after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("late Open did not finish")
	}
	deadline := time.Now().Add(time.Second)
	for instance.closed.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if instance.closed.Load() != 1 || runs.Load() != 0 || hub.Status("one").Health != "down" {
		t.Fatal("late Open leaked active generation")
	}
}

func TestShouldBoundCloseWhenRunIgnoresCancellation(t *testing.T) {
	data := connectionStore(t, "one")
	entered, release := make(chan struct{}), make(chan struct{})
	instance := &fakeInstance{run: func(context.Context) { close(entered); <-release }}
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return instance, nil }), nil)
	waitSignal(t, entered)
	start := time.Now()
	hub.Close()
	if time.Since(start) > 300*time.Millisecond || instance.closed.Load() != 1 {
		t.Fatal("uncooperative Run bypassed bounded close")
	}
	close(release)
}

func TestShouldObserveProviderRunStatusAfterInitialFailure(t *testing.T) {
	data := connectionStore(t, "one")
	var recovered atomic.Bool
	instance := &fakeInstance{readStatus: func() Status {
		if recovered.Load() {
			return Status{Health: "healthy"}
		}
		return Status{Health: "down", LastError: "remote credential"}
	}}
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return instance, nil }), nil)
	if hub.Status("one").Health != "down" {
		t.Fatal("initial provider error missing")
	}
	recovered.Store(true)
	if got := hub.Status("one"); got.Health != "healthy" || got.LastError != "" {
		t.Fatalf("stale initial status=%+v", got)
	}
}
