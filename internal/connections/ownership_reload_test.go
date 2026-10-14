package connections

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldNotReplaceStateWriterAfterCloseBeforeSyncDone(t *testing.T) {
	data := connectionStore(t, "one", "two")
	initial := json.RawMessage(`{"fingerprint":"old"}`)
	latest := json.RawMessage(`{"fingerprint":"replacement"}`)
	if err := data.UpdateConnectionState(context.Background(), "org", "one", initial); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	old := &fakeInstance{sync: func(context.Context) (Status, error) {
		close(entered)
		<-release
		return Status{}, data.UpdateConnectionState(context.Background(), "org", "one", latest)
	}}
	var opens atomic.Int32
	provider := testProvider(func(cfg Config, _ Deps) (Instance, error) {
		if cfg.Record.ID == "two" {
			return &fakeInstance{}, nil
		}
		if opens.Add(1) == 1 {
			return old, nil
		}
		if string(cfg.Record.State) != string(latest) {
			return nil, errors.New("stale private state")
		}
		return &fakeInstance{sync: func(ctx context.Context) (Status, error) {
			return Status{}, data.UpdateConnectionState(ctx, "org", "one", cfg.Record.State)
		}}, nil
	})
	hub := testHub(t, data, provider, nil)
	hub.mu.RLock()
	oldRun := hub.runs["one"]
	hub.mu.RUnlock()
	hub.Trigger("one")
	waitSignal(t, entered)
	start := time.Now()
	if err := hub.Reload(context.Background(), "one"); !errors.Is(err, ErrDrainPending) {
		t.Fatalf("replacement before sync completion: %v", err)
	}
	if time.Since(start) > 300*time.Millisecond || opens.Load() != 1 || old.closed.Load() != 1 {
		t.Fatal("retirement bound or writer isolation violated")
	}
	waitSignal(t, oldRun.closeDone)
	for attempt := 0; attempt < 3; attempt++ {
		if err := hub.Reload(context.Background(), "one"); !errors.Is(err, ErrDrainPending) {
			t.Fatalf("retry opened an overlapping writer: %v", err)
		}
	}
	if _, err := hub.Sync(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	waitSignal(t, oldRun.done)
	for attempt := 0; attempt < 3; attempt++ {
		if err := hub.Reload(context.Background(), "one"); err != nil {
			t.Fatalf("completed generation still blocked retry: %v", err)
		}
		if _, err := hub.Sync(context.Background(), "one"); err != nil {
			t.Fatal(err)
		}
	}
	record, err := data.GetConnection(context.Background(), "one")
	if err != nil || string(record.State) != string(latest) {
		t.Fatalf("durable state regressed: %+v %v", record, err)
	}
}

func TestShouldRefreshWholeRecordAndSecretsAfterRetirement(t *testing.T) {
	data := connectionStore(t, "one")
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan Config, 2)
	updated := make(chan error, 1)
	var opens atomic.Int32
	provider := testProvider(func(cfg Config, _ Deps) (Instance, error) {
		opened <- cfg
		if opens.Add(1) > 1 {
			return &fakeInstance{}, nil
		}
		return &fakeInstance{close: func() {
			record, err := data.GetConnection(context.Background(), "one")
			if err == nil {
				record.Name = "fresh-name"
				record.Config = json.RawMessage(`{"fresh":true}`)
				record.SecretsEnc, err = box.Seal([]byte(`{"token":"fresh-secret"}`), SecretContext("org", "one"))
				if err == nil {
					err = data.UpdateConnection(context.Background(), record)
				}
			}
			updated <- err
		}}, nil
	})
	hub := testHub(t, data, provider, box)
	<-opened
	if err := hub.Reload(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	fresh := <-opened
	if fresh.Record.Name != "fresh-name" || string(fresh.Record.Config) != `{"fresh":true}` || fresh.Secrets["token"] != "fresh-secret" || len(fresh.Record.SecretsEnc) != 0 {
		t.Fatalf("record and secrets came from different snapshots: name=%s config=%s", fresh.Record.Name, fresh.Record.Config)
	}
}

type changedReloadIdentityStore struct {
	store.DataStore
	field string
	reads atomic.Int32
}

func (d *changedReloadIdentityStore) GetConnection(ctx context.Context, id string) (*store.Connection, error) {
	record, err := d.DataStore.GetConnection(ctx, id)
	if err == nil && d.reads.Add(1) == 3 {
		switch d.field {
		case "id":
			record.ID = "different"
		case "org":
			record.OrgID = "different"
		case "type":
			record.Type = "different"
		}
	}
	return record, err
}

func TestShouldRejectChangedIdentityAfterRetirement(t *testing.T) {
	for _, field := range []string{"id", "org", "type"} {
		t.Run(field, func(t *testing.T) {
			data := &changedReloadIdentityStore{DataStore: connectionStore(t, "one"), field: field}
			var opens atomic.Int32
			hub := testHub(t, data, testProvider(func(Config, Deps) (Instance, error) {
				opens.Add(1)
				return &fakeInstance{}, nil
			}), nil)
			if err := hub.Reload(context.Background(), "one"); err == nil || opens.Load() != 1 {
				t.Fatalf("changed identity opened: error=%v opens=%d", err, opens.Load())
			}
		})
	}
}
