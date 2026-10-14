package connections

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldClearConcurrentAdmissionFailureAtSuccessfulInstall(t *testing.T) {
	data := connectionStore(t, "one")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var opens atomic.Int32
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) {
		if opens.Add(1) == 2 {
			close(entered)
			<-release
		}
		return &fakeInstance{status: Status{Health: "up"}}, nil
	}), nil)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	row.Name = "saved-new"
	if err := data.UpdateConnection(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- hub.ReloadIfCurrent(ctx, *row) }()
	waitSignal(t, entered)
	canceled, cancelOther := context.WithCancel(context.Background())
	cancelOther()
	if err := hub.ReloadIfCurrent(canceled, *row); !errors.Is(err, context.Canceled) {
		t.Fatalf("second reload=%v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatalf("first reload=%v", err)
	}
	if status, err := hub.SyncIfCurrent(ctx, *row); err != nil || status.Health != "up" {
		t.Fatalf("loaded generation sync=%+v %v", status, err)
	}
	if status := hub.StatusIfCurrent(*row); status.Health != "up" || status.LastError != "" {
		t.Fatalf("successful current generation still shadowed by concurrent admission failure: %+v", status)
	}
}

func blockedSuccessfulInstall(t *testing.T, initial Status) (*Hub, store.DataStore, *store.Connection, <-chan error, func()) {
	t.Helper()
	data := connectionStore(t, "one")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(release) }) }
	var opens atomic.Int32
	hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) {
		if opens.Add(1) == 2 {
			close(entered)
			<-release
		}
		return &fakeInstance{status: initial}, nil
	}), nil)
	t.Cleanup(releaseOpen)
	row, err := data.GetConnection(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	row.Name = "saved-new"
	if err := data.UpdateConnection(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- hub.ReloadIfCurrent(ctx, *row) }()
	waitSignal(t, entered)
	return hub, data, row, done, releaseOpen
}

func TestShouldNotPoisonSuccessfulInstallWithDelayedAdmissionDiagnosticRead(t *testing.T) {
	hub, data, row, installed, releaseOpen := blockedSuccessfulInstall(t, Status{Health: "up"})
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseRead := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRead)
	var reads atomic.Int32
	hub.opts.Deps.Data = diagnosticReadConnectionData{DataStore: data, read: func(ctx context.Context, id string) (*store.Connection, error) {
		if reads.Add(1) == 1 {
			captured, err := data.GetConnection(ctx, id)
			close(entered)
			<-release
			return captured, err
		}
		return data.GetConnection(ctx, id)
	}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- hub.ReloadIfCurrent(canceled, *row) }()
	waitSignal(t, entered)
	releaseOpen()
	if err := <-installed; err != nil {
		t.Fatal(err)
	}
	if status, err := hub.SyncIfCurrent(context.Background(), *row); err != nil || status.Health != "up" {
		t.Fatalf("installed sync=%+v %v", status, err)
	}
	releaseRead()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if status := hub.StatusIfCurrent(*row); status.Health != "up" || status.LastError != "" {
		t.Fatalf("delayed diagnostic poisoned installed runtime=%+v", status)
	}
}

func TestShouldPreserveNewerSavedSnapshotFailureWhilePublishingOlderRuntime(t *testing.T) {
	hub, data, older, installed, releaseOpen := blockedSuccessfulInstall(t, Status{Health: "up"})
	newer := *older
	newer.Name = "newer-saved"
	if err := data.UpdateConnection(context.Background(), &newer); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hub.ReloadIfCurrent(canceled, newer); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	releaseOpen()
	if err := <-installed; err != nil {
		t.Fatal(err)
	}
	if status := hub.StatusIfCurrent(newer); status.Health != "down" || !strings.Contains(status.LastError, "canceled before admission") {
		t.Fatalf("older installation erased newer saved failure=%+v", status)
	}
	if _, err := hub.SyncIfCurrent(context.Background(), newer); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("older runtime acted on newer unchecked snapshot=%v", err)
	}
}

func TestShouldPreserveCurrentProviderErrorWhenClearingAdmissionFailure(t *testing.T) {
	hub, _, row, installed, releaseOpen := blockedSuccessfulInstall(t, Status{Health: "down", LastError: "provider credential-value"})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hub.ReloadIfCurrent(canceled, *row); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	releaseOpen()
	if err := <-installed; err != nil {
		t.Fatal(err)
	}
	if status := hub.StatusIfCurrent(*row); status.Health != "down" || status.LastError != "connection provider reported an error" || strings.Contains(status.LastError, "credential-value") {
		t.Fatalf("current provider error erased or leaked=%+v", status)
	}
}
