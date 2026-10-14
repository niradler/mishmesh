package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	DriverDocker  = "docker"
	DriverCompose = "compose"
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
	Config            json.RawMessage
	Ready             Readiness
	Requires          []string
	FallbackReadyAddr string
	IdleTimeout       time.Duration
	StartTimeout      time.Duration
}

func FromRecord(rec *store.Service) (Definition, error) {
	return FromRecordWithRegistry(rec, builtinRegistry())
}

func FromRecordWithRegistry(rec *store.Service, registry *Registry) (Definition, error) {
	if rec == nil {
		return Definition{}, errors.New("service record is required")
	}
	driver, config, ready := rec.Driver, rec.Config, rec.Ready
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
