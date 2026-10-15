package services

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/typedfields"
)

func TestShouldRejectUnknownDriver(t *testing.T) {
	registry := testRegistry(t)
	if _, ok := registry.Lookup("remote-docker"); ok {
		t.Fatal("unknown driver registered")
	}
	if _, ok := registry.Lookup("exec"); ok {
		t.Fatal("legacy driver registered")
	}
	if err := registry.Register(DockerType{}); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if err := registry.Register(nil); err == nil {
		t.Fatal("nil registration accepted")
	}
}

func TestShouldDescribeDriversDeterministically(t *testing.T) {
	registry := testRegistry(t)
	first := registry.Descriptors()
	var names []string
	for _, descriptor := range first {
		names = append(names, descriptor.Type)
		if descriptor.Label == "" || descriptor.Help == "" {
			t.Fatalf("missing descriptor text: %+v", descriptor)
		}
	}
	if !reflect.DeepEqual(names, []string{"command", "compose", "docker"}) {
		t.Fatalf("drivers = %v", names)
	}
	first[0].Fields[0].Key = "mutated"
	if reflect.DeepEqual(first, registry.Descriptors()) {
		t.Fatal("caller mutated registered descriptor")
	}
}

func TestShouldValidateRegisteredDriverConfigs(t *testing.T) {
	tests := []struct {
		driver string
		config string
		valid  bool
	}{
		{"docker", `{"containers":["web"]}`, true},
		{"docker", `{"label":"app=web"}`, true},
		{"docker", `{}`, false},
		{"docker", `{"containers":["bad name"]}`, false},
		{"docker", `{"containers":[""]}`, false},
		{"docker", `{"label":" "}`, false},
		{"docker", `{"label":"x","extra":"secret"}`, false},
		{"docker", `{"label":"x","label":"y"}`, false},
		{"docker", `{"label":"x"} {}`, false},
		{"compose", `{"project":"stack"}`, true},
		{"compose", `{"project":"stack","services":["web"],"file":"/a.yml","bin":"docker"}`, true},
		{"compose", `{"project":"stack","bin":"custom"}`, true},
		{"compose", `{"project":" "}`, false},
		{"compose", `{"project":"stack","services":[""]}`, false},
		{"compose", `{"project":"stack","bin":4}`, false},
		{"command", `{"start":["true"],"stop":["true"]}`, true},
		{"command", `{"start":["a",""],"stop":["b"],"env":{"A":"secret"}}`, true},
		{"command", `{"start":["a"]}`, false},
		{"command", `{"start":[" "],"stop":["b"]}`, false},
		{"command", `{"start":["a"],"stop":["b"],"status":[""]}`, false},
		{"command", `{"start":["a"],"stop":["b"],"env":{"A=B":"secret"}}`, false},
		{"command", `{"start":["a"],"stop":["b"],"env":{"A":"\u0000"}}`, false},
		{"command", `{"start":["a\u0000"],"stop":["b"]}`, false},
		{"command", `{"start":["a"],"stop":["b"],"env":{"A":"x","A":"y"}}`, false},
	}
	for _, test := range tests {
		t.Run(test.driver+test.config, func(t *testing.T) {
			driver, ok := testRegistry(t).Lookup(test.driver)
			if !ok {
				t.Fatal("driver missing")
			}
			err := driver.Validate(json.RawMessage(test.config))
			if (err == nil) != test.valid {
				t.Fatalf("error = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestShouldMarkAndEnforceHostFields(t *testing.T) {
	registry := testRegistry(t)
	for name, keys := range map[string][]string{"command": {"start", "stop", "status"}, "compose": {"file", "bin"}} {
		driver, _ := registry.Lookup(name)
		for _, key := range keys {
			marked := false
			for _, field := range driver.Describe().Fields {
				if field.Key == key {
					marked = field.HostCommand
				}
			}
			if !marked {
				t.Fatalf("%s.%s missing host marker", name, key)
			}
		}
	}
	for _, config := range []string{`{"project":"p","file":"/x.yml"}`, `{"project":"p","bin":"custom"}`, `{"project":"p","bin":" "}`, `{"project":"p","file":" "}`} {
		driver, _ := registry.Lookup("compose")
		if _, err := driver.New(json.RawMessage(config), Deps{}); !errors.Is(err, ErrHostCommandsOff) {
			t.Fatalf("config %s: %v", config, err)
		}
	}
	driver, _ := registry.Lookup("command")
	config := json.RawMessage(`{"start":["true"],"stop":["true"]}`)
	if _, err := driver.New(config, Deps{}); !errors.Is(err, ErrHostCommandsOff) {
		t.Fatalf("command gate = %v", err)
	}
	if _, err := driver.New(config, Deps{HostCommands: true, Docker: func() (*dockerClient, error) { t.Fatal("command resolved Docker"); return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	compose, _ := registry.Lookup("compose")
	for _, field := range compose.Describe().Fields {
		if field.Key == "bin" && len(field.Default) != 0 {
			t.Fatal("binary has unconditional default")
		}
	}
	host, err := typedfields.UsesHostCommands(compose.Describe().Fields, json.RawMessage(`{"project":"p"}`))
	if err != nil || host {
		t.Fatalf("API-only compose host = %v, %v", host, err)
	}
}

func TestShouldBuildCustomRegisteredDriver(t *testing.T) {
	registry := testRegistry(t)
	if err := registry.Register(testDriverType{}); err != nil {
		t.Fatal(err)
	}
	host := &hostDrivers{registry: registry, dockerHost: "ftp://bad"}
	driver, err := host.build(Definition{Driver: "test", OrgID: "org_a", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := driver.State(context.Background()); err != nil || state != Running {
		t.Fatalf("state = %s, %v", state, err)
	}
}

func TestShouldRejectInvalidDriverDescriptors(t *testing.T) {
	for _, descriptor := range []DriverDescriptor{
		{Type: ""},
		{Type: " docker "},
		{Type: "bad", Fields: []typedfields.Field{{Key: "x", Kind: "unknown"}}},
		{Type: "bad", Fields: []typedfields.Field{{Key: "x", Kind: "text"}, {Key: "x", Kind: "text"}}},
		{Type: "bad", Fields: []typedfields.Field{{Key: "x", Kind: "number", Default: json.RawMessage(`"private"`)}}},
	} {
		if err := NewRegistry().Register(descriptorDriverType{descriptor: descriptor}); err == nil {
			t.Fatal("invalid descriptor accepted")
		}
	}
	var missing *descriptorDriverType
	if err := NewRegistry().Register(missing); err == nil {
		t.Fatal("typed nil accepted")
	}
}

func TestShouldBuildRegistryDockerAndComposeWithExistingBehavior(t *testing.T) {
	for _, test := range []struct{ driver, config string }{
		{"docker", `{"containers":["web"]}`},
		{"compose", `{"project":"stack","services":["web"]}`},
	} {
		t.Run(test.driver, func(t *testing.T) {
			engine := newFakeEngine(t, &fakeContainer{ID: "1", Name: "web", State: "exited", Labels: map[string]string{composeProjectLabel: "stack", composeServiceLabel: "web"}})
			host := &hostDrivers{registry: testRegistry(t), dockerHost: engine.host()}
			driver, err := host.build(Definition{Driver: test.driver, Config: json.RawMessage(test.config)})
			if err != nil {
				t.Fatal(err)
			}
			if err := driver.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			if state, err := driver.State(context.Background()); err != nil || state != Running {
				t.Fatalf("state=%s error=%v", state, err)
			}
			if err := driver.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if engine.stateOf("1") != "exited" {
				t.Fatal("container not stopped")
			}
		})
	}
}

func TestShouldDefaultComposeBinaryOnlyForFile(t *testing.T) {
	engine := newFakeEngine(t)
	host := &hostDrivers{registry: testRegistry(t), dockerHost: engine.host(), hostCommands: true}
	driver, err := host.build(Definition{Driver: DriverCompose, Config: json.RawMessage(`{"project":"stack","file":"/stack.yml"}`)})
	if err != nil {
		t.Fatal(err)
	}
	compose := driver.(*composeDriver)
	var called []string
	compose.run = func(ctx context.Context, argv []string, dir string, env map[string]string) (string, error) {
		called = append([]string(nil), argv...)
		return "", nil
	}
	if err := compose.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(called, []string{"docker", "compose", "-f", "/stack.yml", "-p", "stack", "up", "-d"}) {
		t.Fatalf("argv=%v", called)
	}
	if _, err := (&hostDrivers{registry: testRegistry(t)}).build(Definition{Driver: "unknown", Config: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("unknown driver constructed")
	}
}

func TestShouldRedactCommandEnvironmentUsingDescriptor(t *testing.T) {
	driver, _ := testRegistry(t).Lookup(DriverCommand)
	redacted, err := typedfields.Redact(driver.Describe().Fields, json.RawMessage(`{"start":["a"],"stop":["b"],"env":{"TOKEN":"private-test-value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-test-value") || !strings.Contains(string(encoded), "TOKEN") {
		t.Fatal("environment redaction failed")
	}
}

type descriptorDriverType struct {
	testDriverType
	descriptor DriverDescriptor
}

func (d descriptorDriverType) Describe() DriverDescriptor { return d.descriptor }

type testDriverType struct{}

func (testDriverType) Describe() DriverDescriptor {
	return DriverDescriptor{Type: "test", Label: "Test"}
}
func (testDriverType) Validate(config json.RawMessage) error {
	return typedfields.Validate(nil, config)
}
func (testDriverType) New(config json.RawMessage, deps Deps) (Driver, error) {
	if deps.OrgID != "org_a" {
		return nil, errors.New("missing org")
	}
	return &fakeDriver{state: Running}, nil
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	if len(registry.Descriptors()) != 0 {
		t.Fatal("new registry is not empty")
	}
	for _, driver := range []DriverType{DockerType{}, ComposeType{}, CommandType{}} {
		if err := registry.Register(driver); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}
