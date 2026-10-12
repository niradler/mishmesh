package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

const (
	DriverDocker  = "docker"
	DriverCompose = "compose"
	DriverExec    = "exec"
	DriverCommand = "command"

	DefaultDockerHost   = "unix:///var/run/docker.sock"
	DefaultStartTimeout = 120 * time.Second
	DefaultComposeBin   = "docker"

	maxTimeoutSeconds = 7 * 24 * 60 * 60
)

var (
	NamePattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9-_.]{0,62}$`)
	ErrHostCommandsOff   = errors.New("this service runs host commands, which are disabled on this server (MISHMESH_HOST_COMMANDS)")
	containerNamePattern = regexp.MustCompile(`^/?[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
)

type Spec struct {
	Docker   *DockerSpec  `json:"docker,omitempty"`
	Compose  *ComposeSpec `json:"compose,omitempty"`
	Exec     *ExecSpec    `json:"exec,omitempty"`
	ReadyTCP string       `json:"ready_tcp,omitempty"`
}

type DockerSpec struct {
	Containers []string `json:"containers,omitempty"`
	Label      string   `json:"label,omitempty"`
}

type ComposeSpec struct {
	Project  string   `json:"project"`
	File     string   `json:"file,omitempty"`
	Bin      string   `json:"bin,omitempty"`
	Services []string `json:"services,omitempty"`
}

type ExecSpec struct {
	Start  []string          `json:"start"`
	Stop   []string          `json:"stop"`
	Status []string          `json:"status,omitempty"`
	Dir    string            `json:"dir,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

type Definition struct {
	ID                string
	OrgID             string
	Name              string
	Driver            string
	Spec              Spec
	Config            json.RawMessage
	Ready             Readiness
	Requires          []string
	FallbackReadyAddr string
	IdleTimeout       time.Duration
	StartTimeout      time.Duration
}

func UsesHostCommands(driver string, spec Spec) bool {
	driverType, config, err := legacyDriverConfig(driver, spec)
	if err != nil {
		return true
	}
	uses, err := typedfields.UsesHostCommands(driverType.Describe().Fields, config)
	return err != nil || uses
}

func legacyDriverConfig(driver string, spec Spec) (DriverType, json.RawMessage, error) {
	var driverType DriverType
	var value any
	switch driver {
	case DriverDocker:
		driverType, value = DockerType{}, spec.Docker
	case DriverCompose:
		driverType, value = ComposeType{}, spec.Compose
	case DriverExec, DriverCommand:
		driverType, value = CommandType{}, spec.Exec
	default:
		return nil, nil, errors.New("unknown driver")
	}
	config, err := json.Marshal(value)
	return driverType, config, err
}

func DecodeSpec(raw []byte) (Spec, error) {
	var spec Spec
	if len(bytes.TrimSpace(raw)) == 0 {
		return spec, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("spec: %w", err)
	}
	return spec, nil
}

func Normalize(driver string, spec Spec) Spec {
	out := Spec{ReadyTCP: strings.TrimSpace(spec.ReadyTCP)}
	switch driver {
	case DriverDocker:
		if spec.Docker != nil {
			out.Docker = &DockerSpec{Containers: trimAll(spec.Docker.Containers), Label: strings.TrimSpace(spec.Docker.Label)}
		}
	case DriverCompose:
		if spec.Compose != nil {
			c := *spec.Compose
			c.Project = strings.TrimSpace(c.Project)
			c.File = strings.TrimSpace(c.File)
			c.Bin = strings.TrimSpace(c.Bin)
			c.Services = trimAll(c.Services)
			if c.File != "" && c.Bin == "" {
				c.Bin = DefaultComposeBin
			}
			if c.File == "" {
				c.Bin = ""
			}
			out.Compose = &c
		}
	case DriverExec:
		if spec.Exec != nil {
			e := *spec.Exec
			e.Dir = strings.TrimSpace(e.Dir)
			out.Exec = &e
		}
	}
	return out
}

func Validate(name, driver string, spec Spec, idleSeconds, startSeconds int) error {
	if !NamePattern.MatchString(name) {
		return fmt.Errorf("name must be 1-63 lowercase letters, digits, '-', '_' or '.', starting with a letter or digit")
	}
	if idleSeconds < -1 || idleSeconds > maxTimeoutSeconds {
		return fmt.Errorf("idle_timeout_seconds must be between -1 and %d", maxTimeoutSeconds)
	}
	if startSeconds < 0 || startSeconds > maxTimeoutSeconds {
		return fmt.Errorf("start_timeout_seconds must be between 0 and %d", maxTimeoutSeconds)
	}
	if spec.ReadyTCP != "" {
		if err := ValidateReadiness(Readiness{TCP: spec.ReadyTCP}); err != nil {
			return fmt.Errorf("ready_tcp: %w", err)
		}
	}
	blocks := 0
	for _, set := range []bool{spec.Docker != nil, spec.Compose != nil, spec.Exec != nil} {
		if set {
			blocks++
		}
	}
	if blocks > 1 {
		return errors.New("spec must contain only the block for its driver")
	}
	switch driver {
	case DriverDocker:
		if spec.Docker == nil || (len(spec.Docker.Containers) == 0 && spec.Docker.Label == "") {
			return errors.New("docker services need container names or a label")
		}
		for _, c := range spec.Docker.Containers {
			if !containerNamePattern.MatchString(c) {
				return fmt.Errorf("invalid container name %q", c)
			}
		}
	case DriverCompose:
		if spec.Compose == nil || spec.Compose.Project == "" {
			return errors.New("compose services need a project name")
		}
	case DriverExec:
		if spec.Exec == nil || len(spec.Exec.Start) == 0 || len(spec.Exec.Stop) == 0 {
			return errors.New("exec services need start and stop commands")
		}
		for _, argv := range [][]string{spec.Exec.Start, spec.Exec.Stop, spec.Exec.Status} {
			if len(argv) > 0 && strings.TrimSpace(argv[0]) == "" {
				return errors.New("exec commands must start with a program")
			}
		}
	case "":
		return errors.New("driver is required")
	default:
		return fmt.Errorf("unknown driver %q (want docker, compose or exec)", driver)
	}
	return nil
}

func FromRecord(rec *store.Service) (Definition, error) {
	return FromRecordWithRegistry(rec, builtinRegistry())
}

func FromRecordWithRegistry(rec *store.Service, registry *Registry) (Definition, error) {
	if rec == nil {
		return Definition{}, errors.New("service record is required")
	}
	driver, config, ready := rec.Driver, rec.Config, rec.Ready
	var spec Spec
	if len(bytes.TrimSpace(config)) == 0 {
		var err error
		driver, config, ready, err = store.MigrateService(driver, rec.Spec)
		if err != nil {
			return Definition{}, fmt.Errorf("service %s: %w", rec.ID, err)
		}
		spec, err = DecodeSpec(rec.Spec)
		if err != nil {
			return Definition{}, fmt.Errorf("service %s: %w", rec.ID, err)
		}
		if err := Validate(rec.Name, rec.Driver, spec, rec.IdleTimeoutSeconds, rec.StartTimeoutSeconds); err != nil {
			return Definition{}, fmt.Errorf("service %s: %w", rec.ID, err)
		}
	}
	if !NamePattern.MatchString(rec.Name) {
		return Definition{}, errors.New("invalid service name")
	}
	if rec.IdleTimeoutSeconds < -1 || rec.IdleTimeoutSeconds > maxTimeoutSeconds || rec.StartTimeoutSeconds < 0 || rec.StartTimeoutSeconds > maxTimeoutSeconds {
		return Definition{}, errors.New("service timeout is out of range")
	}
	if registry == nil {
		return Definition{}, errors.New("driver registry is required")
	}
	driverType, ok := registry.Lookup(driver)
	if !ok {
		return Definition{}, fmt.Errorf("unknown driver %q", driver)
	}
	if err := driverType.Validate(config); err != nil {
		return Definition{}, fmt.Errorf("service %s: %w", rec.ID, err)
	}
	readiness, err := DecodeReadiness(ready)
	if err != nil {
		return Definition{}, fmt.Errorf("service %s: %w", rec.ID, err)
	}
	start := time.Duration(rec.StartTimeoutSeconds) * time.Second
	if start <= 0 {
		start = DefaultStartTimeout
	}
	return Definition{
		ID:           rec.ID,
		OrgID:        rec.OrgID,
		Name:         rec.Name,
		Driver:       driver,
		Spec:         spec,
		Config:       append(json.RawMessage(nil), config...),
		Ready:        readiness,
		Requires:     append([]string(nil), rec.Requires...),
		IdleTimeout:  time.Duration(rec.IdleTimeoutSeconds) * time.Second,
		StartTimeout: start,
	}, nil
}

func builtinRegistry() *Registry {
	registry := NewRegistry()
	for _, driver := range []DriverType{DockerType{}, ComposeType{}, CommandType{}} {
		if err := registry.Register(driver); err != nil {
			panic(err)
		}
	}
	return registry
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
