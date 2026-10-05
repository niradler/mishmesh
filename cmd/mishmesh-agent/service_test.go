package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestServiceConfig(t *testing.T) {
	tests := []struct {
		name     string
		exec     string
		config   string
		user     string
		wantArgs []string
	}{
		{name: "defaults", exec: "/usr/local/bin/mishmesh-agent", config: "/etc/mishmesh/agent.yml", wantArgs: []string{"start", "--config", "/etc/mishmesh/agent.yml"}},
		{name: "with user", exec: "/opt/mm", config: "/home/u/agent.yml", user: "mishmesh", wantArgs: []string{"start", "--config", "/home/u/agent.yml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := serviceConfig(tt.exec, tt.config, tt.user)
			if cfg.Name != serviceName || cfg.Executable != tt.exec || cfg.UserName != tt.user {
				t.Fatalf("cfg = %+v", cfg)
			}
			if !reflect.DeepEqual(cfg.Arguments, tt.wantArgs) {
				t.Fatalf("args = %v, want %v", cfg.Arguments, tt.wantArgs)
			}
		})
	}
}

func TestServiceCmdDryRunInstall(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.yml")
	body := "gateway: ws://127.0.0.1:8081\ntoken: abc\ntunnels:\n  web:\n    proto: http\n    addr: 3000\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := serviceCmd([]string{"install", "--config", cfgPath, "--dry-run", "--user", "svc"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dry run: service install", "mishmesh-agent", cfgPath, "user:       svc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q\n%s", want, out.String())
		}
	}
}

func TestServiceCmdErrors(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yml")
	if err := os.WriteFile(bad, []byte("tunnels:\n  x:\n    proto: ftp\n    addr: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no action", args: nil, want: "usage"},
		{name: "unknown action", args: []string{"explode"}, want: "unknown service action"},
		{name: "invalid config", args: []string{"install", "--config", bad, "--dry-run"}, want: "invalid config"},
		{name: "missing config", args: []string{"install", "--config", filepath.Join(t.TempDir(), "nope.yml"), "--dry-run"}, want: "read config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := serviceCmd(tt.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildStartOptionsPrecedence(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "agent.yml")
	body := "gateway: ws://file:1\ntoken: filetoken\ntunnels:\n  web:\n    proto: http\n    addr: 3000\n  db:\n    proto: tcp\n    addr: 5432\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISHMESH_GATEWAY_URL", "ws://env:1")
	t.Setenv("MISHMESH_TOKEN", "envtoken")

	tests := []struct {
		name      string
		gateway   string
		token     string
		names     []string
		wantGW    string
		wantTok   string
		wantSpecs int
		wantErr   string
	}{
		{name: "file beats env", wantGW: "ws://file:1", wantTok: "filetoken", wantSpecs: 2},
		{name: "flags beat file", gateway: "ws://flag:1", token: "flagtoken", wantGW: "ws://flag:1", wantTok: "flagtoken", wantSpecs: 2},
		{name: "selected names", names: []string{"db"}, wantGW: "ws://file:1", wantTok: "filetoken", wantSpecs: 1},
		{name: "unknown name", names: []string{"nope"}, wantErr: "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := buildStartOptions(cfgPath, tt.gateway, tt.token, "", tt.names)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opts.GatewayURL != tt.wantGW || opts.Token != tt.wantTok || len(opts.Endpoints) != tt.wantSpecs {
				t.Fatalf("opts = %q %q %d", opts.GatewayURL, opts.Token, len(opts.Endpoints))
			}
		})
	}
}

func TestBuildStartOptionsRequiresToken(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "agent.yml")
	if err := os.WriteFile(cfgPath, []byte("tunnels:\n  web:\n    proto: http\n    addr: 3000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISHMESH_TOKEN", "")
	if _, err := buildStartOptions(cfgPath, "", "", "", nil); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("err = %v", err)
	}
}
