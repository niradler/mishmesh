package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
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

func TestShouldShareOneCompleteKeyAcrossConcurrentFreshConstructors(t *testing.T) {
	for range 8 {
		path := filepath.Join(t.TempDir(), "shared.key")
		const constructors = 64
		start := make(chan struct{})
		keys := make(chan []byte, constructors)
		errors := make(chan error, constructors)
		var wait sync.WaitGroup
		for range constructors {
			wait.Add(1)
			go func() { defer wait.Done(); <-start; key, err := LoadOrCreateKey(path); keys <- key; errors <- err }()
		}
		close(start)
		wait.Wait()
		close(keys)
		close(errors)
		for err := range errors {
			if err != nil {
				t.Fatal(err)
			}
		}
		var expected []byte
		for key := range keys {
			if expected == nil {
				expected = key
			}
			if len(key) != keySize || !bytes.Equal(key, expected) {
				t.Fatal("concurrent constructors used different or partial keys")
			}
		}
		persisted, err := LoadOrCreateKey(path)
		if err != nil || !bytes.Equal(persisted, expected) {
			t.Fatalf("persisted winner changed %v", err)
		}
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
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "not-a-key" {
		t.Fatal("invalid existing key was replaced")
	}
}

func TestShouldPublishOnlyCompleteClosedKeyAndRespectCompetingWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atomic.key")
	var winner []byte
	key, err := loadOrCreateKey(path, func(prepared, destination string) error {
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("destination visible before publication")
		}
		raw, err := os.ReadFile(prepared)
		if err != nil {
			t.Fatal(err)
		}
		if decoded, err := decodeKey(prepared, raw); err != nil || len(decoded) != keySize {
			t.Fatalf("publication exposed partial key %v", err)
		}
		winner, err = LoadOrCreateKey(destination)
		if err != nil {
			t.Fatal(err)
		}
		return os.Link(prepared, destination)
	})
	if err != nil || !bytes.Equal(key, winner) {
		t.Fatalf("losing constructor replaced winner %v", err)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".mishmesh-key-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("private key staging files remain %v %v", files, err)
	}
}

func TestShouldFailClosedWhenAtomicKeyPublicationUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unpublished.key")
	failure := errors.New("atomic links unavailable")
	if _, err := loadOrCreateKey(path, func(string, string) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("publication failure lost %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial final key left after publication failure")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".mishmesh-key-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("failed publication left staging key")
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
