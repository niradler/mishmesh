package cloudflare

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestShouldNeverAdoptUnchangedSelectedResources(t *testing.T) {
	for _, preexistingIngress := range []bool{true, false} {
		t.Run(map[bool]string{true: "all-preexisting", false: "new-ingress"}[preexistingIngress], func(t *testing.T) {
			fixture := newOwnedFixture(t)
			if preexistingIngress {
				fixture.fake.setConfig(t, `{"ingress":[{"hostname":"a.example.com","service":"http://127.0.0.1:8080"},{"service":"http_status:404"}]}`)
			}
			fixture.fake.setApp(t, `{"self_hosted_domains":["a.example.com"],"destinations":[{"type":"public","uri":"a.example.com"}]}`)
			fixture.fake.records["z-example"] = []DNSRecord{{ID: "external", Type: "CNAME", Name: "a.example.com", Content: testTunnel + ".cfargotunnel.com", Proxied: true}}
			fixture.sync(t)
			record, err := fixture.data.GetConnection(context.Background(), "a")
			if err != nil {
				t.Fatal(err)
			}
			var state ownershipState
			if err := json.Unmarshal(record.State, &state); err != nil {
				t.Fatal(err)
			}
			owned := state.Routes["a.example.com"]
			if len(owned.DNS) != 0 || owned.Access != "" || preexistingIngress && owned.Ingress != "" || !preexistingIngress && owned.Ingress == "" {
				t.Fatalf("resource ownership was inferred from observation: %+v", owned)
			}
			if fixture.fake.dnsPosts != 0 || fixture.fake.appPuts != 0 || preexistingIngress && fixture.fake.configPuts != 0 || !preexistingIngress && fixture.fake.configPuts != 1 {
				t.Fatalf("unexpected initial writes: ingress=%d dns=%d access=%d", fixture.fake.configPuts, fixture.fake.dnsPosts, fixture.fake.appPuts)
			}
			fixture.data.DataStore.Close()
			reopened, err := sqlite.Open(fixture.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			fixture.data.DataStore = reopened
			fixture.data.endpoints[0].Policy.Access = nil
			fixture.sync(t)
			if fixture.fake.dnsDeletes != 0 || !accessHasHost(AccessApp(fixture.fake.app), "a.example.com") {
				t.Fatal("preexisting DNS or Access entry was pruned")
			}
			present := false
			for _, rule := range fixture.fake.ingressServices() {
				if rule == "a.example.com|"+testOrigin {
					present = true
				}
			}
			if present != preexistingIngress {
				t.Fatalf("ingress retained=%v preexisting=%v", present, preexistingIngress)
			}
		})
	}
}

type reloadOwnershipData struct {
	*ownershipData
	pauseRead atomic.Bool
	read      chan struct{}
	readNext  chan struct{}
	save      chan struct{}
	saveNext  chan struct{}
	saved     chan struct{}
	saves     atomic.Int32
}

func (d *reloadOwnershipData) GetConnection(ctx context.Context, id string) (*store.Connection, error) {
	record, err := d.ownershipData.GetConnection(ctx, id)
	if d.pauseRead.CompareAndSwap(true, false) {
		close(d.read)
		<-d.readNext
	}
	return record, err
}

func (d *reloadOwnershipData) UpdateConnectionState(ctx context.Context, org, id string, state json.RawMessage) error {
	if d.saves.Add(1) == 1 {
		close(d.save)
		<-d.saveNext
		err := d.ownershipData.UpdateConnectionState(ctx, org, id, state)
		close(d.saved)
		return err
	}
	return d.ownershipData.UpdateConnectionState(ctx, org, id, state)
}

type observedOwnershipProvider struct {
	connections.Provider
	opened chan *providerInstance
}

func (p observedOwnershipProvider) Open(cfg connections.Config, deps connections.Deps) (connections.Instance, error) {
	instance, err := p.Provider.Open(cfg, deps)
	if err == nil {
		p.opened <- instance.(*providerInstance)
	}
	return instance, err
}

func waitOwnershipSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("ownership synchronization deadline exceeded")
	}
}

func TestShouldRefreshOwnershipAfterRetiredCloudflareGenerationSaves(t *testing.T) {
	fixture := newOwnedFixture(t)
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(fixture.config.Secrets)
	fixture.config.Record.SecretsEnc, err = box.Seal(plain, connections.SecretContext("org", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.data.UpdateConnection(context.Background(), &fixture.config.Record); err != nil {
		t.Fatal(err)
	}
	data := &reloadOwnershipData{ownershipData: fixture.data, read: make(chan struct{}), readNext: make(chan struct{}), save: make(chan struct{}), saveNext: make(chan struct{}), saved: make(chan struct{})}
	opened := make(chan *providerInstance, 3)
	registry := connections.NewRegistry()
	if err := registry.Register(observedOwnershipProvider{Provider: fixture.open, opened: opened}); err != nil {
		t.Fatal(err)
	}
	hub := connections.NewHub(connections.HubOptions{Registry: registry, Deps: connections.Deps{Data: data, Box: box, Origin: testOrigin, Log: quietProviderLog}, DrainTimeout: time.Second})
	defer hub.Close()
	if err := hub.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-opened
	waitOwnershipSignal(t, data.save)
	data.pauseRead.Store(true)
	reloaded := make(chan error, 1)
	go func() { reloaded <- hub.Reload(context.Background(), "a") }()
	waitOwnershipSignal(t, data.read)
	close(data.saveNext)
	waitOwnershipSignal(t, data.saved)
	committed, err := fixture.data.GetConnection(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	close(data.readNext)
	if err := <-reloaded; err != nil {
		t.Fatal(err)
	}
	replacement := <-opened
	if _, err := hub.Sync(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	expected, err := openOwnership(committed.State, replacement.publisher.cfg, fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	wanted := expected.state.Routes["a.example.com"]
	replacement.publisher.syncMu.Lock()
	got := replacement.publisher.ownership.state.Routes["a.example.com"]
	got.DNS = append([]string(nil), got.DNS...)
	replacement.publisher.syncMu.Unlock()
	if wanted.Ingress == "" || len(wanted.DNS) == 0 || wanted.Access == "" || resourceFingerprint(got) != resourceFingerprint(wanted) {
		t.Fatalf("new generation lost committed fingerprints: got=%+v want=%+v", got, wanted)
	}
	persisted, err := fixture.data.GetConnection(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	var state ownershipState
	if err := json.Unmarshal(persisted.State, &state); err != nil || resourceFingerprint(state.Routes["a.example.com"]) != resourceFingerprint(wanted) {
		t.Fatalf("durable fingerprints regressed: %+v %v", state, err)
	}
}
