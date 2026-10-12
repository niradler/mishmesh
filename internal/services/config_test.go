package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const specYAML = `
orgs: [org_default]
docker:
  host: unix:///var/run/docker.sock
defaults:
  idle_timeout: 30m
  start_timeout: 120s
services:
  portainer:
    driver: docker
    docker: { containers: [portainer] }
    idle_timeout: 2h
  affine:
    driver: compose
    compose: { project: affine, file: /opt/homelab/stacks/affine/compose.yaml }
    ready: { tcp: "127.0.0.1:3010" }
  notebook:
    driver: exec
    exec:
      start: ["/usr/local/bin/notebook-up"]
      stop:  ["/usr/local/bin/notebook-down"]
      status: ["/usr/local/bin/notebook-status"]
      dir: /srv/notebook
      env: { FOO: bar }
  never-sleep:
    driver: docker
    docker: { label: "mishmesh.service=cockpit" }
    idle_timeout: 0
`

func TestParseSpecExample(t *testing.T) {
	cfg, err := Parse([]byte(specYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Services) != 4 {
		t.Fatalf("services = %d, want 4", len(cfg.Services))
	}
	portainer := cfg.Services["portainer"]
	if portainer.IdleTimeout != 2*time.Hour || portainer.StartTimeout != 120*time.Second {
		t.Errorf("portainer timeouts = %v/%v", portainer.IdleTimeout, portainer.StartTimeout)
	}
	affine := cfg.Services["affine"]
	if affine.IdleTimeout != 30*time.Minute {
		t.Errorf("affine idle = %v, want 30m", affine.IdleTimeout)
	}
	if affine.Ready == nil || affine.Ready.TCP != "127.0.0.1:3010" {
		t.Errorf("affine ready = %+v", affine.Ready)
	}
	if affine.Compose.Bin != "docker" {
		t.Errorf("compose bin = %q, want docker", affine.Compose.Bin)
	}
	notebook := cfg.Services["notebook"]
	if notebook.Exec.Dir != "/srv/notebook" || notebook.Exec.Env["FOO"] != "bar" || len(notebook.Exec.Status) != 1 {
		t.Errorf("notebook exec = %+v", notebook.Exec)
	}
	if got := cfg.Services["never-sleep"].IdleTimeout; got != 0 {
		t.Errorf("never-sleep idle = %v, want 0", got)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte("services:\n  a:\n    driver: docker\n    docker: {containers: [a]}\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Orgs) != 1 || cfg.Orgs[0] != "org_default" {
		t.Errorf("orgs = %v", cfg.Orgs)
	}
	if cfg.Docker.Host != "unix:///var/run/docker.sock" {
		t.Errorf("host = %q", cfg.Docker.Host)
	}
	if got := cfg.Services["a"]; got.IdleTimeout != 0 || got.StartTimeout != 120*time.Second {
		t.Errorf("timeouts = %v/%v", got.IdleTimeout, got.StartTimeout)
	}
}

func TestParseEmpty(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Services) != 0 {
		t.Errorf("services = %d", len(cfg.Services))
	}
}

func TestParseValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"bad name uppercase", "services:\n  Bad:\n    driver: docker\n    docker: {containers: [a]}\n", "name must match"},
		{"bad name leading dash", "services:\n  -bad:\n    driver: docker\n    docker: {containers: [a]}\n", "name must match"},
		{"name too long", "services:\n  " + strings.Repeat("a", 64) + ":\n    driver: docker\n    docker: {containers: [a]}\n", "name must match"},
		{"missing driver", "services:\n  a:\n    docker: {containers: [a]}\n", "driver is required"},
		{"unknown driver", "services:\n  a:\n    driver: vm\n", "unknown driver"},
		{"docker without block", "services:\n  a:\n    driver: docker\n", "requires a docker block"},
		{"docker empty block", "services:\n  a:\n    driver: docker\n    docker: {}\n", "containers or label"},
		{"compose without block", "services:\n  a:\n    driver: compose\n", "requires a compose block"},
		{"compose without project", "services:\n  a:\n    driver: compose\n    compose: {file: x}\n", "needs project"},
		{"exec without block", "services:\n  a:\n    driver: exec\n", "requires an exec block"},
		{"exec without stop", "services:\n  a:\n    driver: exec\n    exec: {start: [x]}\n", "non-empty start and stop"},
		{"exec without start", "services:\n  a:\n    driver: exec\n    exec: {stop: [x]}\n", "non-empty start and stop"},
		{"bad duration", "defaults:\n  idle_timeout: soon\nservices: {}\n", "parse services yaml"},
		{"negative idle", "services:\n  a:\n    driver: docker\n    docker: {label: x=y}\n    idle_timeout: -1m\n", "must not be negative"},
		{"zero start timeout", "services:\n  a:\n    driver: docker\n    docker: {label: x=y}\n    start_timeout: 0s\n", "start_timeout must be positive"},
		{"unknown field", "services:\n  a:\n    driver: docker\n    docker: {label: x=y}\n    idle: 1m\n", "parse services yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseValidNames(t *testing.T) {
	for _, name := range []string{"a", "0abc", "a-b_c.d", strings.Repeat("a", 63)} {
		yaml := "services:\n  " + name + ":\n    driver: docker\n    docker: {label: x=y}\n"
		if _, err := Parse([]byte(yaml)); err != nil {
			t.Errorf("name %q: %v", name, err)
		}
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.yaml")
	if err := os.WriteFile(path, []byte(specYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.Services) != 4 {
		t.Errorf("services = %d", len(cfg.Services))
	}
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("expected error for missing file")
	}
}
