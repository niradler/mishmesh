package orgsecret

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newVault(t *testing.T) (*Vault, store.DataStore) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	for _, id := range []string{"org_a", "org_b"} {
		if err := data.CreateOrg(context.Background(), &store.Org{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(data, box), data
}

func TestVaultSealsAndOpens(t *testing.T) {
	v, data := newVault(t)
	ctx := context.Background()
	sec, err := v.Put(ctx, "org_a", "api", []byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	if sec.Version != 1 || bytes.Contains(sec.ValueEnc, []byte("s3cret")) {
		t.Fatalf("put = %+v", sec)
	}
	got, err := v.Open(ctx, "org_a", "api")
	if err != nil || string(got) != "s3cret" {
		t.Fatalf("open = %q %v", got, err)
	}
	if _, err := v.Open(ctx, "org_b", "api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org open err = %v", err)
	}
	stored, err := data.GetOrgSecret(ctx, "org_a", "api")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.PutOrgSecret(ctx, &store.OrgSecret{OrgID: "org_b", Name: "api", ValueEnc: stored.ValueEnc, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(ctx, "org_b", "api"); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("transplanted ciphertext err = %v, want decrypt failure", err)
	}
}

func TestVaultRejectsBadInput(t *testing.T) {
	v, _ := newVault(t)
	ctx := context.Background()
	tests := []struct {
		name, secret string
		value        []byte
		want         error
	}{
		{"bad name", "Bad Name", []byte("x"), ErrInvalidName},
		{"empty", "api", nil, ErrInvalidValue},
		{"newline", "api", []byte("a\r\nX-Evil: 1"), ErrInvalidValue},
		{"nul", "api", []byte("a\x00"), ErrInvalidValue},
		{"too long", "api", bytes.Repeat([]byte("a"), MaxValueBytes+1), ErrInvalidValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := v.Put(ctx, "org_a", tt.secret, tt.value); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	var nilVault *Vault
	if _, err := nilVault.Open(ctx, "org_a", "api"); !errors.Is(err, ErrNoBox) {
		t.Fatalf("nil vault err = %v", err)
	}
}
