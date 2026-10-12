package services

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DriverDocker  = "docker"
	DriverCompose = "compose"
	DriverExec    = "exec"

	DefaultOrg          = "org_default"
	DefaultDockerHost   = "unix:///var/run/docker.sock"
	DefaultStartTimeout = 120 * time.Second
	DefaultComposeBin   = "docker"
)

var serviceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-_.]{0,62}$`)

type Config struct {
	Orgs     []string
	Docker   DockerConfig
	Defaults Defaults
	Services map[string]ServiceConfig
}

type DockerConfig struct {
	Host string
}

type Defaults struct {
	IdleTimeout  time.Duration
	StartTimeout time.Duration
}

type ServiceConfig struct {
	Driver       string
	Docker       *DockerSpec
	Compose      *ComposeSpec
	Exec         *ExecSpec
	Ready        *ReadySpec
	IdleTimeout  time.Duration
	StartTimeout time.Duration
}

type DockerSpec struct {
	Containers []string
	Label      string
}

type ComposeSpec struct {
	Project  string
	File     string
	Bin      string
	Services []string
}

type ExecSpec struct {
	Start  []string
	Stop   []string
	Status []string
	Dir    string
	Env    map[string]string
}

type ReadySpec struct {
	TCP string
}

type yamlDuration time.Duration

func (d *yamlDuration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a scalar", node.Line)
	}
	if node.Value == "0" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q (use 0 or a value like 30m)", node.Line, node.Value)
	}
	*d = yamlDuration(parsed)
	return nil
}

type rawConfig struct {
	Orgs   []string `yaml:"orgs"`
	Docker struct {
		Host string `yaml:"host"`
	} `yaml:"docker"`
	Defaults struct {
		IdleTimeout  *yamlDuration `yaml:"idle_timeout"`
		StartTimeout *yamlDuration `yaml:"start_timeout"`
	} `yaml:"defaults"`
	Services map[string]rawService `yaml:"services"`
}

type rawService struct {
	Driver string `yaml:"driver"`
	Docker *struct {
		Containers []string `yaml:"containers"`
		Label      string   `yaml:"label"`
	} `yaml:"docker"`
	Compose *struct {
		Project  string   `yaml:"project"`
		File     string   `yaml:"file"`
		Bin      string   `yaml:"bin"`
		Services []string `yaml:"services"`
	} `yaml:"compose"`
	Exec *struct {
		Start  []string          `yaml:"start"`
		Stop   []string          `yaml:"stop"`
		Status []string          `yaml:"status"`
		Dir    string            `yaml:"dir"`
		Env    map[string]string `yaml:"env"`
	} `yaml:"exec"`
	Ready *struct {
		TCP string `yaml:"tcp"`
	} `yaml:"ready"`
	IdleTimeout  *yamlDuration `yaml:"idle_timeout"`
	StartTimeout *yamlDuration `yaml:"start_timeout"`
}

func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read services file: %w", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("services file %s: %w", path, err)
	}
	return cfg, nil
}

func Parse(data []byte) (*Config, error) {
	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse services yaml: %w", err)
	}

	cfg := &Config{
		Orgs:     raw.Orgs,
		Docker:   DockerConfig{Host: raw.Docker.Host},
		Services: make(map[string]ServiceConfig, len(raw.Services)),
	}
	if len(cfg.Orgs) == 0 {
		cfg.Orgs = []string{DefaultOrg}
	}
	if cfg.Docker.Host == "" {
		cfg.Docker.Host = DefaultDockerHost
	}
	if raw.Defaults.IdleTimeout != nil {
		cfg.Defaults.IdleTimeout = time.Duration(*raw.Defaults.IdleTimeout)
	}
	cfg.Defaults.StartTimeout = DefaultStartTimeout
	if raw.Defaults.StartTimeout != nil {
		cfg.Defaults.StartTimeout = time.Duration(*raw.Defaults.StartTimeout)
	}
	if cfg.Defaults.IdleTimeout < 0 {
		return nil, errors.New("defaults.idle_timeout must not be negative")
	}
	if cfg.Defaults.StartTimeout <= 0 {
		return nil, errors.New("defaults.start_timeout must be positive")
	}

	names := make([]string, 0, len(raw.Services))
	for name := range raw.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc, err := buildService(name, raw.Services[name], cfg.Defaults)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		cfg.Services[name] = svc
	}
	return cfg, nil
}

func buildService(name string, raw rawService, defaults Defaults) (ServiceConfig, error) {
	if !serviceNamePattern.MatchString(name) {
		return ServiceConfig{}, errors.New("name must match " + serviceNamePattern.String())
	}
	svc := ServiceConfig{
		Driver:       raw.Driver,
		IdleTimeout:  defaults.IdleTimeout,
		StartTimeout: defaults.StartTimeout,
	}
	if raw.IdleTimeout != nil {
		svc.IdleTimeout = time.Duration(*raw.IdleTimeout)
	}
	if raw.StartTimeout != nil {
		svc.StartTimeout = time.Duration(*raw.StartTimeout)
	}
	if svc.IdleTimeout < 0 {
		return ServiceConfig{}, errors.New("idle_timeout must not be negative")
	}
	if svc.StartTimeout <= 0 {
		return ServiceConfig{}, errors.New("start_timeout must be positive")
	}
	if raw.Ready != nil && raw.Ready.TCP != "" {
		svc.Ready = &ReadySpec{TCP: raw.Ready.TCP}
	}

	switch raw.Driver {
	case DriverDocker:
		if raw.Docker == nil {
			return ServiceConfig{}, errors.New("driver docker requires a docker block")
		}
		if len(raw.Docker.Containers) == 0 && raw.Docker.Label == "" {
			return ServiceConfig{}, errors.New("docker block needs containers or label")
		}
		svc.Docker = &DockerSpec{Containers: raw.Docker.Containers, Label: raw.Docker.Label}
	case DriverCompose:
		if raw.Compose == nil {
			return ServiceConfig{}, errors.New("driver compose requires a compose block")
		}
		if raw.Compose.Project == "" {
			return ServiceConfig{}, errors.New("compose block needs project")
		}
		bin := raw.Compose.Bin
		if bin == "" {
			bin = DefaultComposeBin
		}
		svc.Compose = &ComposeSpec{
			Project:  raw.Compose.Project,
			File:     raw.Compose.File,
			Bin:      bin,
			Services: raw.Compose.Services,
		}
	case DriverExec:
		if raw.Exec == nil {
			return ServiceConfig{}, errors.New("driver exec requires an exec block")
		}
		if len(raw.Exec.Start) == 0 || len(raw.Exec.Stop) == 0 {
			return ServiceConfig{}, errors.New("exec block needs non-empty start and stop")
		}
		svc.Exec = &ExecSpec{
			Start:  raw.Exec.Start,
			Stop:   raw.Exec.Stop,
			Status: raw.Exec.Status,
			Dir:    raw.Exec.Dir,
			Env:    raw.Exec.Env,
		}
	case "":
		return ServiceConfig{}, errors.New("driver is required")
	default:
		return ServiceConfig{}, fmt.Errorf("unknown driver %q (want docker, compose or exec)", raw.Driver)
	}
	return svc, nil
}
