package connections

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldPreserveDelayedNewerAdmissionDiagnosticAfterOlderPublication(t *testing.T) {
	hub, data, older, installed, releaseOpen := blockedSuccessfulInstall(t, Status{Health: "up"})
	newer := *older
	newer.Name = "newer-saved"
	if err := data.UpdateConnection(context.Background(), &newer); err != nil {
		t.Fatal(err)
	}
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
	go func() { done <- hub.ReloadIfCurrent(canceled, newer) }()
	waitSignal(t, entered)
	releaseOpen()
	if err := <-installed; err != nil {
		t.Fatal(err)
	}
	releaseRead()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	status := hub.StatusIfCurrent(newer)
	if status.Health != "down" {
		t.Fatalf("newer unloaded saved snapshot looks healthy=%+v", status)
	}
	if !strings.Contains(status.LastError, "canceled before admission") {
		t.Fatalf("older runtime publication lost newer saved admission cause: snapshot=%+v raw=%+v", status, hub.Status(newer.ID))
	}
}

func pausedNewerAdmissionDiagnostic(t *testing.T, hub *Hub, data store.DataStore, expected store.Connection) (<-chan error, func()) {
	t.Helper()
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
	go func() { done <- hub.ReloadIfCurrent(canceled, expected) }()
	waitSignal(t, entered)
	return done, releaseRead
}

func TestShouldFenceDelayedNewerAdmissionDiagnosticAfterCurrentSuccessDeletionOrClosure(t *testing.T) {
	for _, outcome := range []string{"current-success", "deletion", "closure", "current-row-changed"} {
		t.Run(outcome, func(t *testing.T) {
			hub, data, older, installed, releaseOpen := blockedSuccessfulInstall(t, Status{Health: "up"})
			newer := *older
			newer.Name = "newer-saved"
			if err := data.UpdateConnection(context.Background(), &newer); err != nil {
				t.Fatal(err)
			}
			diagnostic, releaseRead := pausedNewerAdmissionDiagnostic(t, hub, data, newer)
			releaseOpen()
			if err := <-installed; err != nil {
				t.Fatal(err)
			}
			switch outcome {
			case "current-success":
				if err := hub.ReloadIfCurrent(context.Background(), newer); err != nil {
					t.Fatal(err)
				}
			case "deletion":
				if err := data.DeleteConnection(context.Background(), newer.ID); err != nil {
					t.Fatal(err)
				}
				if err := hub.Reload(context.Background(), newer.ID); err != nil {
					t.Fatal(err)
				}
			case "closure":
				hub.Close()
			case "current-row-changed":
				changed := newer
				changed.Name = "latest-saved"
				if err := data.UpdateConnection(context.Background(), &changed); err != nil {
					t.Fatal(err)
				}
			}
			releaseRead()
			if err := <-diagnostic; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			hub.mu.RLock()
			_, retained := hub.reloadFailures[newer.ID]
			hub.mu.RUnlock()
			if retained {
				t.Fatalf("%s resurrected obsolete saved-row diagnostic: %+v", outcome, hub.Status(newer.ID))
			}
			if outcome == "current-success" {
				if status := hub.StatusIfCurrent(newer); status.Health != "up" || status.LastError != "" {
					t.Fatalf("current success poisoned: %+v", status)
				}
			}
		})
	}
}
