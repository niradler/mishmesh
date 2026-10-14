package connections

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldRetainFailedReloadStatusAfterCommittedUpdate(t *testing.T) {
	data := connectionStore(t, "one")
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) {
		return &fakeInstance{status: Status{Health: "up"}}, nil
	}), nil)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	row.Name = "saved-new-config"
	if err := data.UpdateConnection(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hub.ReloadIfCurrent(ctx, *row); err == nil {
		t.Fatal("expected canceled reload")
	}
	if status := hub.Status(row.ID); status.Health == "up" || status.LastError == "" {
		t.Fatalf("saved row reload failed but later status claims healthy: %+v", status)
	}
	if _, err := hub.SyncIfCurrent(context.Background(), *row); err != store.ErrConflict {
		t.Fatalf("old generation sync = %v", err)
	}
}

func TestShouldKeepAdmissionFailureUntilSuccessAndClearItOnDeletion(t *testing.T) {
	data := connectionStore(t, "one")
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return &fakeInstance{status: Status{Health: "up"}}, nil }), nil)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	row.Name = "saved-admission-timeout"
	if err := data.UpdateConnection(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if err := hub.enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = hub.ReloadIfCurrent(ctx, *row)
	cancel()
	hub.leave()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reload=%v", err)
	}
	if status := hub.Status(row.ID); status.Health != "down" || !strings.Contains(status.LastError, "timed out before admission") {
		t.Fatalf("pending=%+v", status)
	}
	if _, err := hub.Sync(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	if status := hub.Status(row.ID); status.Health != "down" || !strings.Contains(status.LastError, "timed out") {
		t.Fatalf("old background sync erased saved failure=%+v", status)
	}
	if err := hub.ReloadIfCurrent(context.Background(), *row); err != nil {
		t.Fatal(err)
	}
	if status := hub.StatusIfCurrent(*row); status.Health != "up" || status.LastError != "" {
		t.Fatalf("successful reload did not clear failure=%+v", status)
	}
	row.Name = "saved-again"
	if err := data.UpdateConnection(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_ = hub.ReloadIfCurrent(ctx, *row)
	if err := data.DeleteConnection(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	if err := hub.Reload(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	hub.mu.RLock()
	_, retained := hub.reloadFailures[row.ID]
	hub.mu.RUnlock()
	if retained {
		t.Fatal("deletion retained a saved-row failure marker")
	}
	if status := hub.Status(row.ID); !strings.Contains(status.LastError, "not loaded") || strings.Contains(status.LastError, "saved") {
		t.Fatalf("deleted status=%+v", status)
	}
}

type diagnosticReadConnectionData struct {
	store.DataStore
	read func(context.Context, string) (*store.Connection, error)
}

func (data diagnosticReadConnectionData) GetConnection(ctx context.Context, id string) (*store.Connection, error) {
	return data.read(ctx, id)
}

func TestShouldNotOverwriteNewerSuccessfulGenerationWithAdmissionFailure(t *testing.T) {
	data := connectionStore(t, "one")
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return &fakeInstance{status: Status{Health: "up"}}, nil }), nil)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	row.Name = "first-saved"
	if err := data.UpdateConnection(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	hub.opts.Deps.Data = diagnosticReadConnectionData{DataStore: data, read: func(ctx context.Context, id string) (*store.Connection, error) {
		if reads.Add(1) == 1 {
			captured, err := data.GetConnection(ctx, id)
			close(entered)
			select {
			case <-release:
				return captured, err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return data.GetConnection(ctx, id)
	}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- hub.ReloadIfCurrent(canceled, *row) }()
	waitSignal(t, entered)
	newer := *row
	newer.Name = "newer-saved"
	if err := data.UpdateConnection(context.Background(), &newer); err != nil {
		t.Fatal(err)
	}
	if err := hub.ReloadIfCurrent(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if status := hub.Status(newer.ID); status.Health != "up" || status.LastError != "" {
		t.Fatalf("older failure overwrote newer success=%+v", status)
	}
	if err := hub.ReloadIfCurrent(canceled, *row); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if status := hub.StatusIfCurrent(newer); status.Health != "up" || status.LastError != "" {
		t.Fatalf("stale retry overwrote success=%+v", status)
	}
}

func TestShouldExposeSnapshotPendingWhenDiagnosticReadFails(t *testing.T) {
	data := connectionStore(t, "one")
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) { return &fakeInstance{status: Status{Health: "up"}}, nil }), nil)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	row.Name = "saved-but-diagnostic-store-unavailable"
	if err := data.UpdateConnection(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	hub.opts.Deps.Data = diagnosticReadConnectionData{DataStore: data, read: func(context.Context, string) (*store.Connection, error) {
		return nil, errors.New("temporary diagnostic read failure")
	}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hub.ReloadIfCurrent(canceled, *row); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	hub.opts.Deps.Data = data
	if status := hub.StatusIfCurrent(*row); status.Health == "up" || !strings.Contains(status.LastError, "saved connection configuration is not loaded") {
		t.Fatalf("unloaded saved snapshot looks healthy=%+v", status)
	}
}

func TestShouldClearForgedHostProvenanceFromProviderStatusAndSync(t *testing.T) {
	data := connectionStore(t, "one")
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) {
		return &fakeInstance{status: WithHostDiagnostic(Status{Health: "down"}, HostOpenFailed)}, nil
	}), nil)
	if status := hub.Status("one"); status.hostDiagnostic != "" || status.LastError != "connection provider reported an error" {
		t.Fatalf("provider Status forged phase=%+v", status)
	}
	expected, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	status, err := hub.SyncIfCurrent(context.Background(), *expected)
	if err != nil || status.hostDiagnostic != "" || status.LastError != "connection provider reported an error" {
		t.Fatalf("provider Sync forged phase=%+v err=%v", status, err)
	}
}
