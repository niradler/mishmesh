package token

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seed(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func validClaims(now time.Time) Claims {
	return Claims{Type: TypeAccess, SessionID: "tsn_1", TokenID: "tid_1", OrgID: "org_1", IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	k, err := NewKeyring(seed(1))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	raw, err := k.Sign(validClaims(now))
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Verify(raw, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != validClaims(now) {
		t.Fatalf("claims = %+v", got)
	}
}

func TestVerifyRejects(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	k, _ := NewKeyring(seed(1))
	other, _ := NewKeyring(seed(2))
	good, _ := k.Sign(validClaims(now))
	foreign, _ := other.Sign(validClaims(now))
	parts := strings.Split(good, ".")
	tamperedPayload := parts[0] + "." + parts[1] + "." + parts[2] + "x." + parts[3]
	expired := validClaims(now)
	expired.ExpiresAt = now.Unix()
	expiredRaw, _ := k.Sign(expired)

	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"empty", "", ErrMalformed},
		{"wrong prefix", "jwt1." + strings.TrimPrefix(good, "mmt1."), ErrMalformed},
		{"too long", good + strings.Repeat("a", MaxLength), ErrMalformed},
		{"bad charset", strings.Replace(good, "mmt1.", "mmt1+", 1), ErrMalformed},
		{"extra part", good + ".x", ErrMalformed},
		{"empty part", "mmt1.." + parts[2] + "." + parts[3], ErrMalformed},
		{"unknown key", foreign, ErrSignature},
		{"tampered payload", tamperedPayload, ErrSignature},
		{"expired", expiredRaw, ErrExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := k.Verify(tc.raw, now); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSignRequiresClaims(t *testing.T) {
	k, _ := NewKeyring(seed(1))
	if _, err := k.Sign(Claims{Type: TypeAccess}); err == nil {
		t.Fatal("expected error for incomplete claims")
	}
}

func TestRotationVerifiesOldKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old, _ := NewKeyring(seed(1))
	raw, _ := old.Sign(validClaims(now))
	rotated, err := NewKeyring(seed(2), seed(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Verify(raw, now); err != nil {
		t.Fatalf("old token rejected after rotation: %v", err)
	}
	fresh, _ := rotated.Sign(validClaims(now))
	if _, err := old.Verify(fresh, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("old keyring accepted new-key token: %v", err)
	}
}

func TestLoadOrCreateKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token-signing.key")
	first, err := LoadOrCreateKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.signer.id != second.signer.id {
		t.Fatal("reloaded keyring has a different signing key")
	}
	if err := os.WriteFile(path, []byte("zz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeyring(path); err == nil {
		t.Fatal("expected error for corrupt key file")
	}
}
