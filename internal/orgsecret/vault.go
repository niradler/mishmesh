package orgsecret

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	MaxValueBytes   = 8 << 10
	DefaultCacheTTL = 30 * time.Second
	maxCacheEntries = 4096
)

var (
	ErrInvalidName  = errors.New("orgsecret: invalid secret name")
	ErrInvalidValue = errors.New("orgsecret: value must be 1-8192 bytes without CR, LF or NUL")
	ErrNoBox        = errors.New("orgsecret: secret key unavailable")
)

type secretKey struct{ orgID, name string }

type cachedSecret struct {
	value   []byte
	expires time.Time
}

type Vault struct {
	data   store.DataStore
	box    *secrets.Box
	now    func() time.Time
	ttl    time.Duration
	origin string

	mu     sync.Mutex
	cache  map[secretKey]cachedSecret
	gens   map[secretKey]uint64
	bus    store.EventBus
	busLog *slog.Logger
}

func New(data store.DataStore, box *secrets.Box) *Vault {
	return &Vault{
		data:   data,
		box:    box,
		now:    time.Now,
		ttl:    DefaultCacheTTL,
		origin: store.NewID("node"),
		cache:  map[secretKey]cachedSecret{},
		gens:   map[secretKey]uint64{},
	}
}

func ValidValue(value []byte) bool {
	return len(value) > 0 && len(value) <= MaxValueBytes && !strings.ContainsAny(string(value), "\r\n\x00")
}

func (v *Vault) Put(ctx context.Context, orgID, name string, value []byte) (*store.OrgSecret, error) {
	if v == nil || v.box == nil {
		return nil, ErrNoBox
	}
	if !store.ValidCredentialName(name) {
		return nil, ErrInvalidName
	}
	if !ValidValue(value) {
		return nil, ErrInvalidValue
	}
	sealed, err := v.box.Seal(value, store.OrgSecretContext(orgID, name))
	if err != nil {
		return nil, fmt.Errorf("seal secret %s: %w", name, err)
	}
	sec := &store.OrgSecret{OrgID: orgID, Name: name, ValueEnc: sealed, UpdatedAt: v.now()}
	v.invalidate(secretKey{orgID, name})
	err = v.data.PutOrgSecret(ctx, sec)
	v.invalidateEverywhere(secretKey{orgID, name})
	if err != nil {
		return nil, err
	}
	return sec, nil
}

func (v *Vault) Delete(ctx context.Context, orgID, name string) error {
	if v == nil {
		return ErrNoBox
	}
	v.invalidate(secretKey{orgID, name})
	err := v.data.DeleteOrgSecret(ctx, orgID, name)
	v.invalidateEverywhere(secretKey{orgID, name})
	return err
}

func (v *Vault) Open(ctx context.Context, orgID, name string) ([]byte, error) {
	if v == nil || v.box == nil {
		return nil, ErrNoBox
	}
	key := secretKey{orgID, name}
	if value, ok := v.cached(key); ok {
		return value, nil
	}
	gen := v.generation(key)
	sec, err := v.data.GetOrgSecret(ctx, orgID, name)
	if err != nil {
		return nil, err
	}
	plain, err := v.box.Open(sec.ValueEnc, store.OrgSecretContext(orgID, name))
	if err != nil {
		return nil, fmt.Errorf("open secret %s: %w", name, err)
	}
	v.store(key, gen, plain)
	return plain, nil
}

func (v *Vault) cached(key secretKey) ([]byte, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	entry, ok := v.cache[key]
	if !ok {
		return nil, false
	}
	if !v.now().Before(entry.expires) {
		delete(v.cache, key)
		return nil, false
	}
	return append([]byte(nil), entry.value...), true
}

func (v *Vault) generation(key secretKey) uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.gens[key]
}

func (v *Vault) store(key secretKey, gen uint64, plain []byte) {
	if v.ttl <= 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.gens[key] != gen {
		return
	}
	now := v.now()
	if len(v.cache) >= maxCacheEntries {
		for k, entry := range v.cache {
			if !now.Before(entry.expires) {
				delete(v.cache, k)
			}
		}
	}
	if len(v.cache) >= maxCacheEntries {
		return
	}
	v.cache[key] = cachedSecret{value: append([]byte(nil), plain...), expires: now.Add(v.ttl)}
}

func (v *Vault) invalidate(key secretKey) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.gens[key]++
	delete(v.cache, key)
}
