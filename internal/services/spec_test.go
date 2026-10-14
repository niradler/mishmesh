package services

import (
	"encoding/json"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name, driver, config, ready string
		idle, start                 int
		invalid                     bool
	}{
		{"docker by container", "docker", `{"containers":["portainer"]}`, `{}`, 1800, 60, false},
		{"docker by label", "docker", `{"label":"app=web"}`, `{}`, 0, 0, false},
		{"compose by project", "compose", `{"project":"stack"}`, `{}`, 0, 0, false},
		{"command", "command", `{"start":["a"],"stop":["b"]}`, `{}`, 0, 0, false},
		{"ready tcp", "docker", `{"containers":["portainer"]}`, `{"tcp":"127.0.0.1:9443"}`, 0, 0, false},
		{"Bad Name", "docker", `{"label":"web"}`, `{}`, 0, 0, true},
		{"negative idle", "docker", `{"label":"web"}`, `{}`, -2, 0, true},
		{"huge start", "docker", `{"label":"web"}`, `{}`, 0, maxTimeoutSeconds + 1, true},
		{"ready without port", "docker", `{"label":"web"}`, `{"tcp":"localhost"}`, 0, 0, true},
		{"missing driver", "", `{"label":"web"}`, `{}`, 0, 0, true},
		{"unknown driver", "podman", `{"label":"web"}`, `{}`, 0, 0, true},
		{"docker without selector", "docker", `{}`, `{}`, 0, 0, true},
		{"docker bad container", "docker", `{"containers":["a b"]}`, `{}`, 0, 0, true},
		{"compose without project", "compose", `{}`, `{}`, 0, 0, true},
		{"command without stop", "command", `{"start":["a"]}`, `{}`, 0, 0, true},
		{"command empty program", "command", `{"start":[" "],"stop":["b"]}`, `{}`, 0, 0, true},
		{"two blocks", "docker", `{"containers":["web"],"exec":{}}`, `{}`, 0, 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := strings.ReplaceAll(test.name, " ", "-")
			if test.name == "Bad Name" {
				name = test.name
			}
			_, err := FromRecord(&store.Service{ID: "svc", Name: name, Driver: test.driver, Config: json.RawMessage(test.config), Ready: json.RawMessage(test.ready), IdleTimeoutSeconds: test.idle, StartTimeoutSeconds: test.start})
			if (err != nil) != test.invalid {
				t.Fatalf("invalid=%v error=%v", test.invalid, err)
			}
		})
	}
}

func TestDecodeSpecRejectsUnknownFields(t *testing.T) {
	if _, err := FromRecord(&store.Service{Name: "web", Driver: "docker", Config: json.RawMessage(`{"containers":["a"],"extra":1}`)}); err == nil {
		t.Fatal("unknown field accepted")
	}
	driver, config, ready, err := store.MigrateService("compose", json.RawMessage(`{"compose":{"project":"p","services":["web"]},"ready_tcp":"127.0.0.1:80"}`))
	if err != nil {
		t.Fatal(err)
	}
	def, err := FromRecord(&store.Service{Name: "web", Driver: driver, Config: config, Ready: ready})
	if err != nil || def.Ready.TCP != "127.0.0.1:80" || !strings.Contains(string(config), `"web"`) {
		t.Fatalf("converted=%+v %v", def, err)
	}
	if _, err := FromRecord(&store.Service{Name: "web", Driver: "docker"}); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestShouldPreserveTypedConfigWithoutLegacyNormalization(t *testing.T) {
	config := json.RawMessage(`{"start":["run"," a b "],"stop":["stop"],"env":{"SECRET":"synthetic"}}`)
	def, err := FromRecord(&store.Service{Name: "web", Driver: "command", Config: config})
	if err != nil || string(def.Config) != string(config) {
		t.Fatalf("config changed: %s %v", def.Config, err)
	}
	if _, _, _, err := store.MigrateService("docker", json.RawMessage(`{"docker":{"containers":["a"]},"exec":{}}`)); err == nil {
		t.Fatal("ambiguous legacy blocks accepted")
	}
}

func TestUsesHostCommands(t *testing.T) {
	tests := []struct {
		driver, config string
		want           bool
	}{
		{"command", `{"start":["a"],"stop":["b"]}`, true},
		{"compose", `{"project":"p","file":"/x.yml"}`, true},
		{"compose", `{"project":"p"}`, false},
		{"docker", `{"label":"x"}`, false},
	}
	registry := builtinRegistry()
	for _, test := range tests {
		driver, _ := registry.Lookup(test.driver)
		got, err := typedfields.UsesHostCommands(driver.Describe().Fields, json.RawMessage(test.config))
		if err != nil || got != test.want {
			t.Fatalf("host gate %s=%v %v", test.driver, got, err)
		}
	}
}

func TestFromRecord(t *testing.T) {
	rec := &store.Service{ID: "svc_1", OrgID: "org_a", Name: "portainer", Driver: DriverDocker, Config: json.RawMessage(`{"containers":["portainer"]}`), IdleTimeoutSeconds: 1800}
	def, err := FromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if def.IdleTimeout != 30*time.Minute || def.StartTimeout != DefaultStartTimeout || string(def.Config) != string(rec.Config) {
		t.Fatalf("definition=%+v", def)
	}
	rec.Config = json.RawMessage(`{}`)
	if _, err := FromRecord(rec); err == nil {
		t.Fatal("empty selector accepted")
	}
}

func TestShouldConvertLegacyCommandRecordAndPreferGenericConfig(t *testing.T) {
	driver, config, ready, err := store.MigrateService("exec", json.RawMessage(`{"exec":{"start":["a"],"stop":["b"]},"ready_tcp":"127.0.0.1:8080"}`))
	if err != nil {
		t.Fatal(err)
	}
	record := &store.Service{ID: "svc_1", OrgID: "org_a", Name: "job", Driver: driver, Config: config, Ready: ready}
	definition, err := FromRecord(record)
	if err != nil || definition.Driver != DriverCommand || definition.Ready.TCP != "127.0.0.1:8080" {
		t.Fatalf("definition=%+v %v", definition, err)
	}
	record.Config = json.RawMessage(`{"start":["new"],"stop":["stop"]}`)
	record.Ready = json.RawMessage(`{"tcp":"127.0.0.1:9090"}`)
	record.Requires = []string{"svc_2"}
	definition, err = FromRecordWithRegistry(record, testRegistry(t))
	if err != nil || string(definition.Config) != string(record.Config) || readyTarget(definition, "") != "127.0.0.1:9090" {
		t.Fatalf("definition=%+v %v", definition, err)
	}
	definition.Config[0] = ' '
	definition.Requires[0] = "changed"
	if record.Config[0] != '{' || record.Requires[0] != "svc_2" {
		t.Fatal("definition aliases record")
	}
	if _, err := FromRecordWithRegistry(nil, testRegistry(t)); err == nil {
		t.Fatal("nil record accepted")
	}
	if _, err := FromRecordWithRegistry(record, NewRegistry()); err == nil {
		t.Fatal("unregistered driver accepted")
	}
}

func TestShouldLoadCustomDriverRecordUsingRegistry(t *testing.T) {
	registry := testRegistry(t)
	if err := registry.Register(testDriverType{}); err != nil {
		t.Fatal(err)
	}
	definition, err := FromRecordWithRegistry(&store.Service{ID: "svc_1", OrgID: "org_a", Name: "custom", Driver: "test", Config: json.RawMessage(`{}`)}, registry)
	if err != nil || definition.Driver != "test" {
		t.Fatalf("definition=%+v %v", definition, err)
	}
}

func TestShouldLoadWithDependentsIdleSentinel(t *testing.T) {
	for _, idle := range []int{-2, -1, 0, 1} {
		rec := &store.Service{ID: "svc", OrgID: "org", Name: "svc", Driver: DriverDocker, IdleTimeoutSeconds: idle, Config: json.RawMessage(`{"containers":["web"]}`)}
		def, err := FromRecord(rec)
		if idle < -1 {
			if err == nil {
				t.Fatal("invalid negative timeout accepted")
			}
			continue
		}
		if err != nil || def.IdleTimeout != time.Duration(idle)*time.Second {
			t.Fatalf("idle=%d def=%+v %v", idle, def, err)
		}
	}
}
