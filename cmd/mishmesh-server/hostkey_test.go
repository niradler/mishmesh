package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/config"
)

func TestDefaultHostKeyPath(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Server
		want string
	}{
		{"sqlite file beside the db", config.Server{DataDSN: "/data/mishmesh.db"}, "/data/ssh_host_ed25519.pem"},
		{"relative sqlite", config.Server{DataDSN: "mishmesh.db"}, "ssh_host_ed25519.pem"},
		{"postgres follows the acme cache dir", config.Server{DataDSN: "postgres://u@h/db", ACMECacheDir: "/data/certs"}, "/data/ssh_host_ed25519.pem"},
		{"postgres with trailing slash", config.Server{DataDSN: "postgresql://u@h/db", ACMECacheDir: "/data/certs/"}, "/data/ssh_host_ed25519.pem"},
		{"postgres with default cache dir", config.Server{DataDSN: "postgres://u@h/db", ACMECacheDir: "./certs"}, "ssh_host_ed25519.pem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filepath.ToSlash(defaultHostKeyPath(tt.cfg))
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestLoadOrCreateHostKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.pem")
	first, err := loadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("host key must be stable across loads")
	}
}

func TestLoadOrCreateHostKeyUnwritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "key.pem")
	_, err := loadOrCreateHostKey(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "MISHMESH_SSH_HOST_KEY_FILE") {
		t.Fatalf("error should name the path and the override: %v", err)
	}
}
