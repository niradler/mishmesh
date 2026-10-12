package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	box := newBox(t)
	sealed, err := box.Seal([]byte("tunnel-token"), "cloudflare:org_1:tunnel_token")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("tunnel-token")) {
		t.Fatal("sealed value contains plaintext")
	}
	plain, err := box.Open(sealed, "cloudflare:org_1:tunnel_token")
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "tunnel-token" {
		t.Fatalf("got %q", plain)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	box := newBox(t)
	sealed, err := box.Seal([]byte("secret"), "ctx")
	if err != nil {
		t.Fatal(err)
	}
	flipped := append([]byte(nil), sealed...)
	flipped[len(flipped)-1] ^= 1
	tests := []struct {
		name    string
		sealed  []byte
		context string
	}{
		{"wrong context", sealed, "other"},
		{"flipped byte", flipped, "ctx"},
		{"truncated", sealed[:5], "ctx"},
		{"empty", nil, "ctx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := box.Open(tt.sealed, tt.context); !errors.Is(err, ErrDecrypt) {
				t.Fatalf("want ErrDecrypt, got %v", err)
			}
		})
	}
}

func TestLoadOrCreateKeyPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) != keySize {
		t.Fatal("key not stable across loads")
	}
}

func TestLoadOrCreateKeyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("want error for malformed key file")
	}
}

func TestNewRejectsShortKey(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("want error for 16-byte key")
	}
}

func newBox(t *testing.T) *Box {
	t.Helper()
	box, err := New(bytes.Repeat([]byte{7}, keySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}
