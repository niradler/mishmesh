package orgsecret

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

type loopbackBus struct {
	mu   sync.Mutex
	subs map[string][]func([]byte)
}

func (b *loopbackBus) PublishEvent(_ context.Context, topic string, payload []byte) error {
	b.mu.Lock()
	subs := append([]func([]byte){}, b.subs[topic]...)
	b.mu.Unlock()
	for _, fn := range subs {
		fn(payload)
	}
	return nil
}

func (b *loopbackBus) SubscribeEvents(topic string, fn func([]byte)) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = map[string][]func([]byte){}
	}
	b.subs[topic] = append(b.subs[topic], fn)
	return func() {}, nil
}

type countingStore struct {
	store.DataStore
	mu    sync.Mutex
	reads int
	hook  func()
}

func (c *countingStore) GetOrgSecret(ctx context.Context, orgID, name string) (*store.OrgSecret, error) {
	c.mu.Lock()
	c.reads++
	hook := c.hook
	c.mu.Unlock()
	sec, err := c.DataStore.GetOrgSecret(ctx, orgID, name)
	if hook != nil {
		hook()
	}
	return sec, err
}

func (c *countingStore) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func cachedVault(t *testing.T) (*Vault, *countingStore, *time.Time) {
	t.Helper()
	v, data := newVault(t)
	counting := &countingStore{DataStore: data}
	v.data = counting
	clock := time.Now()
	v.now = func() time.Time { return clock }
	return v, counting, &clock
}

func mustOpen(t *testing.T, v *Vault, name string) string {
	t.Helper()
	got, err := v.Open(context.Background(), "org_a", name)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestOpenServesFromCacheUntilTTL(t *testing.T) {
	v, data, clock := cachedVault(t)
	if _, err := v.Put(context.Background(), "org_a", "api", []byte("one")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got := mustOpen(t, v, "api"); got != "one" {
			t.Fatalf("open = %q", got)
		}
	}
	if data.readCount() != 1 {
		t.Fatalf("store reads = %d, want 1 while cached", data.readCount())
	}
	*clock = clock.Add(DefaultCacheTTL)
	mustOpen(t, v, "api")
	if data.readCount() != 2 {
		t.Fatalf("store reads = %d, want 2 after the TTL", data.readCount())
	}
}

func TestOpenReturnsACopy(t *testing.T) {
	v, _, _ := cachedVault(t)
	if _, err := v.Put(context.Background(), "org_a", "api", []byte("one")); err != nil {
		t.Fatal(err)
	}
	got, _ := v.Open(context.Background(), "org_a", "api")
	got[0] = 'X'
	if again := mustOpen(t, v, "api"); again != "one" {
		t.Fatalf("cache mutated through a returned slice: %q", again)
	}
}

func TestPutAndDeleteInvalidate(t *testing.T) {
	v, _, _ := cachedVault(t)
	ctx := context.Background()
	if _, err := v.Put(ctx, "org_a", "api", []byte("one")); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, v, "api")
	if _, err := v.Put(ctx, "org_a", "api", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if got := mustOpen(t, v, "api"); got != "two" {
		t.Fatalf("open after rotation = %q, want two", got)
	}
	if err := v.Delete(ctx, "org_a", "api"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(ctx, "org_a", "api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("open after delete err = %v, want not found", err)
	}
}

func TestMissesAreNotCached(t *testing.T) {
	v, data, _ := cachedVault(t)
	ctx := context.Background()
	for range 2 {
		if _, err := v.Open(ctx, "org_a", "api"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	}
	if data.readCount() != 2 {
		t.Fatalf("store reads = %d, want 2: misses must not be cached", data.readCount())
	}
}

func TestFillRacingInvalidationIsDiscarded(t *testing.T) {
	v, data, _ := cachedVault(t)
	ctx := context.Background()
	if _, err := v.Put(ctx, "org_a", "api", []byte("old")); err != nil {
		t.Fatal(err)
	}
	data.mu.Lock()
	data.hook = func() {
		data.mu.Lock()
		data.hook = nil
		data.mu.Unlock()
		if _, err := v.Put(ctx, "org_a", "api", []byte("new")); err != nil {
			t.Error(err)
		}
	}
	data.mu.Unlock()
	if got := mustOpen(t, v, "api"); got != "old" {
		t.Fatalf("racing open = %q, want the value it read", got)
	}
	if got := mustOpen(t, v, "api"); got != "new" {
		t.Fatalf("open after the race = %q: a stale fill was cached over the rotation", got)
	}
}

func TestRemoteInvalidationDropsCachedValue(t *testing.T) {
	bus := &loopbackBus{}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	nodeA, data := newVault(t)
	nodeB := New(data, box)
	for _, v := range []*Vault{nodeA, nodeB} {
		stop, err := v.AttachBus(bus, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(stop)
	}
	ctx := context.Background()
	if _, err := nodeA.Put(ctx, "org_a", "api", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if got := mustOpen(t, nodeB, "api"); got != "one" {
		t.Fatalf("node B open = %q", got)
	}
	if _, err := nodeA.Put(ctx, "org_a", "api", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if got := mustOpen(t, nodeB, "api"); got != "two" {
		t.Fatalf("node B served %q after node A rotated the secret", got)
	}
	if err := nodeA.Delete(ctx, "org_a", "api"); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeB.Open(ctx, "org_a", "api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("node B open after delete on node A err = %v", err)
	}
}

func TestDeleteRejectsInvalidNames(t *testing.T) {
	v, _, _ := cachedVault(t)
	before := v.generation()
	if err := v.Delete(context.Background(), "org_a", "Not A Name"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("err = %v, want ErrInvalidName", err)
	}
	if v.generation() != before {
		t.Fatal("an invalid delete must not touch cache state")
	}
}
