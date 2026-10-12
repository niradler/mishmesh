package services

import (
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestValidate(t *testing.T) {
	docker := Spec{Docker: &DockerSpec{Containers: []string{"portainer"}}}
	tests := []struct {
		name    string
		svc     string
		driver  string
		spec    Spec
		idle    int
		start   int
		wantErr string
	}{
		{"docker by container", "portainer", DriverDocker, docker, 1800, 60, ""},
		{"docker by label", "web", DriverDocker, Spec{Docker: &DockerSpec{Label: "app=web"}}, 0, 0, ""},
		{"compose by project", "stack", DriverCompose, Spec{Compose: &ComposeSpec{Project: "stack"}}, 0, 0, ""},
		{"exec", "job", DriverExec, Spec{Exec: &ExecSpec{Start: []string{"a"}, Stop: []string{"b"}}}, 0, 0, ""},
		{"ready tcp", "portainer", DriverDocker, Spec{Docker: docker.Docker, ReadyTCP: "127.0.0.1:9443"}, 0, 0, ""},
		{"bad name", "Bad Name", DriverDocker, docker, 0, 0, "name must"},
		{"negative idle", "x", DriverDocker, docker, -1, 0, "idle_timeout_seconds"},
		{"huge start", "x", DriverDocker, docker, 0, maxTimeoutSeconds + 1, "start_timeout_seconds"},
		{"ready without port", "x", DriverDocker, Spec{Docker: docker.Docker, ReadyTCP: "localhost"}, 0, 0, "ready_tcp"},
		{"missing driver", "x", "", docker, 0, 0, "driver is required"},
		{"unknown driver", "x", "podman", docker, 0, 0, "unknown driver"},
		{"docker without selector", "x", DriverDocker, Spec{Docker: &DockerSpec{}}, 0, 0, "container names or a label"},
		{"docker bad container", "x", DriverDocker, Spec{Docker: &DockerSpec{Containers: []string{"a b"}}}, 0, 0, "invalid container name"},
		{"compose without project", "x", DriverCompose, Spec{Compose: &ComposeSpec{}}, 0, 0, "project"},
		{"exec without stop", "x", DriverExec, Spec{Exec: &ExecSpec{Start: []string{"a"}}}, 0, 0, "start and stop"},
		{"exec empty program", "x", DriverExec, Spec{Exec: &ExecSpec{Start: []string{" "}, Stop: []string{"b"}}}, 0, 0, "program"},
		{"two blocks", "x", DriverDocker, Spec{Docker: docker.Docker, Exec: &ExecSpec{}}, 0, 0, "only the block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.svc, tt.driver, tt.spec, tt.idle, tt.start)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeSpecRejectsUnknownFields(t *testing.T) {
	if _, err := DecodeSpec([]byte(`{"docker":{"containers":["a"]},"extra":1}`)); err == nil {
		t.Fatal("want error for unknown field")
	}
	spec, err := DecodeSpec([]byte(`{"compose":{"project":"p","services":["web"]},"ready_tcp":"127.0.0.1:80"}`))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Compose.Project != "p" || spec.Compose.Services[0] != "web" || spec.ReadyTCP != "127.0.0.1:80" {
		t.Fatalf("decoded = %+v", spec)
	}
	if empty, err := DecodeSpec(nil); err != nil || empty.Docker != nil {
		t.Fatalf("empty spec = %+v, %v", empty, err)
	}
}

func TestNormalizeKeepsOnlyDriverBlock(t *testing.T) {
	in := Spec{
		Docker:   &DockerSpec{Containers: []string{" a ", ""}},
		Compose:  &ComposeSpec{Project: " p ", File: " /srv/x.yml "},
		ReadyTCP: " 127.0.0.1:1 ",
	}
	docker := Normalize(DriverDocker, in)
	if docker.Compose != nil || len(docker.Docker.Containers) != 1 || docker.Docker.Containers[0] != "a" || docker.ReadyTCP != "127.0.0.1:1" {
		t.Fatalf("docker normalize = %+v", docker)
	}
	compose := Normalize(DriverCompose, in)
	if compose.Docker != nil || compose.Compose.Project != "p" || compose.Compose.File != "/srv/x.yml" || compose.Compose.Bin != DefaultComposeBin {
		t.Fatalf("compose normalize = %+v", compose.Compose)
	}
	noFile := Normalize(DriverCompose, Spec{Compose: &ComposeSpec{Project: "p", Bin: "/usr/bin/evil"}})
	if noFile.Compose.Bin != "" {
		t.Fatalf("bin must be dropped without a file: %+v", noFile.Compose)
	}
}

func TestUsesHostCommands(t *testing.T) {
	tests := []struct {
		driver string
		spec   Spec
		want   bool
	}{
		{DriverExec, Spec{Exec: &ExecSpec{}}, true},
		{DriverCompose, Spec{Compose: &ComposeSpec{Project: "p", File: "/x.yml"}}, true},
		{DriverCompose, Spec{Compose: &ComposeSpec{Project: "p"}}, false},
		{DriverDocker, Spec{Docker: &DockerSpec{Label: "x"}}, false},
	}
	for _, tt := range tests {
		if got := UsesHostCommands(tt.driver, tt.spec); got != tt.want {
			t.Errorf("UsesHostCommands(%s, %+v) = %v", tt.driver, tt.spec, got)
		}
	}
}

func TestFromRecord(t *testing.T) {
	rec := &store.Service{ID: "svc_1", OrgID: "org_a", Name: "portainer", Driver: DriverDocker,
		Spec: []byte(`{"docker":{"containers":["portainer"]}}`), IdleTimeoutSeconds: 1800}
	def, err := FromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if def.IdleTimeout != 30*time.Minute || def.StartTimeout != DefaultStartTimeout || def.Spec.Docker.Containers[0] != "portainer" {
		t.Fatalf("definition = %+v", def)
	}
	rec.Spec = []byte(`{"docker":{}}`)
	if _, err := FromRecord(rec); err == nil {
		t.Fatal("want validation error for empty docker selector")
	}
}
