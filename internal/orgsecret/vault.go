package orgsecret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

const MaxValueBytes = 8 << 10

var (
	ErrInvalidName  = errors.New("orgsecret: invalid secret name")
	ErrInvalidValue = errors.New("orgsecret: value must be 1-8192 bytes without CR, LF or NUL")
	ErrNoBox        = errors.New("orgsecret: secret key unavailable")
)

type Vault struct {
	data store.DataStore
	box  *secrets.Box
	now  func() time.Time
}

func New(data store.DataStore, box *secrets.Box) *Vault {
	return &Vault{data: data, box: box, now: time.Now}
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
	if err := v.data.PutOrgSecret(ctx, sec); err != nil {
		return nil, err
	}
	return sec, nil
}

func (v *Vault) Open(ctx context.Context, orgID, name string) ([]byte, error) {
	if v == nil || v.box == nil {
		return nil, ErrNoBox
	}
	sec, err := v.data.GetOrgSecret(ctx, orgID, name)
	if err != nil {
		return nil, err
	}
	plain, err := v.box.Open(sec.ValueEnc, store.OrgSecretContext(orgID, name))
	if err != nil {
		return nil, fmt.Errorf("open secret %s: %w", name, err)
	}
	return plain, nil
}
