package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const keySize = 32

var ErrDecrypt = errors.New("secrets: cannot decrypt value")

type Box struct {
	aead cipher.AEAD
}

func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: gcm: %w", err)
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plaintext []byte, context string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(context)), nil
}

func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrDecrypt
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

func LoadOrCreateKey(path string) ([]byte, error) {
	return loadOrCreateKey(path, os.Link)
}

func loadOrCreateKey(path string, publish func(string, string) error) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return decodeKey(path, raw)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("secrets: read key %s: %w", path, err)
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mishmesh-key-*")
	if err != nil {
		return nil, fmt.Errorf("secrets: create key %s (set MISHMESH_SECRET_KEY_FILE to a writable path): %w", path, err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("secrets: write key %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("secrets: sync key %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("secrets: write key %s: %w", path, err)
	}
	if err := publish(f.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("secrets: read concurrently created key %s: %w", path, err)
			}
			return decodeKey(path, raw)
		}
		return nil, fmt.Errorf("secrets: atomically publish key %s: %w", path, err)
	}
	return key, nil
}

func decodeKey(path string, raw []byte) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != keySize {
		return nil, fmt.Errorf("secrets: key file %s must hold %d hex-encoded bytes", path, keySize)
	}
	return key, nil
}
