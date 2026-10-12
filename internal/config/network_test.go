package config

import (
	"path/filepath"
	"testing"
)

func TestEffectiveSecretKeyFile(t *testing.T) {
	tests := []struct {
		name string
		cfg  Server
		want string
	}{
		{"explicit", Server{SecretKeyFile: "/etc/mishmesh/key", DataDSN: "data/m.db"}, "/etc/mishmesh/key"},
		{"next to sqlite db", Server{DataDSN: filepath.Join("var", "lib", "m.db")}, filepath.Join("var", "lib", secretKeyFileName)},
		{"sqlite uri", Server{DataDSN: "file:" + filepath.Join("data", "m.db") + "?_pragma=busy_timeout(5000)"}, filepath.Join("data", secretKeyFileName)},
		{"bare sqlite file", Server{DataDSN: "m.db"}, secretKeyFileName},
		{"in-memory sqlite", Server{DataDSN: ":memory:"}, secretKeyFileName},
		{"postgres", Server{DataDSN: "postgres://u:p@db/mishmesh"}, secretKeyFileName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveSecretKeyFile(); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestHostCommandsDefault(t *testing.T) {
	tests := []struct {
		signupMode string
		override   string
		want       bool
	}{
		{"invite", "", true},
		{"org", "", false},
		{"org", "true", true},
		{"invite", "false", false},
	}
	for _, tt := range tests {
		t.Run(tt.signupMode+"/"+tt.override, func(t *testing.T) {
			t.Setenv("MISHMESH_SIGNUP_MODE", tt.signupMode)
			t.Setenv("MISHMESH_HOST_COMMANDS", tt.override)
			if got := LoadServer().HostCommands; got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
