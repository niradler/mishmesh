package config

import (
	"path/filepath"
	"testing"
)

func TestValidateAPIPublicURL(t *testing.T) {
	tests := []struct {
		url string
		ok  bool
	}{
		{"", true},
		{"https://api.example.test", true},
		{"http://127.0.0.1:8080/base", true},
		{"api.example.test", false},
		{"ftp://api.example.test", false},
		{"https://api.example.test/?x=1", false},
		{"https:///nohost", false},
	}
	for _, tc := range tests {
		s := baseValid()
		s.APIPublicURL = tc.url
		if err := s.Validate(); (err == nil) != tc.ok {
			t.Errorf("Validate(%q) = %v, want ok=%v", tc.url, err, tc.ok)
		}
	}
}

func TestEffectiveTokenKeyFile(t *testing.T) {
	dir := filepath.Join("data", "dir")
	s := Server{DataDSN: filepath.Join(dir, "mishmesh.db")}
	if got := s.EffectiveTokenKeyFile(); got != filepath.Join(dir, tokenKeyFileName) {
		t.Fatalf("default = %q", got)
	}
	s.SecretKeyFile = filepath.Join("keys", "secret.key")
	if got := s.EffectiveTokenKeyFile(); got != filepath.Join("keys", tokenKeyFileName) {
		t.Fatalf("follows secret key dir = %q", got)
	}
	s.TokenKeyFile = "custom.key"
	if got := s.EffectiveTokenKeyFile(); got != "custom.key" {
		t.Fatalf("override = %q", got)
	}
}
