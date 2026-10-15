package services

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type DockerType struct{}
type ComposeType struct{}
type CommandType struct{}

func (DockerType) Describe() DriverDescriptor {
	return DriverDescriptor{Type: DriverDocker, Label: "Docker", Help: "Start and stop local Docker containers by name or label.", Fields: []typedfields.Field{
		{Key: "containers", Label: "Containers", Kind: "list", Help: "Container names to start and stop."},
		{Key: "label", Label: "Label", Kind: "text", Placeholder: "app=web", Help: "Docker label selector used when no container names are supplied."},
	}}
}

func (ComposeType) Describe() DriverDescriptor {
	return DriverDescriptor{Type: DriverCompose, Label: "Compose", Help: "Start and stop a local Compose project.", Fields: []typedfields.Field{
		{Key: "project", Label: "Project", Kind: "text", Required: true},
		{Key: "services", Label: "Compose services", Kind: "list", Help: "Leave empty for the whole project."},
		{Key: "file", Label: "Compose file", Kind: "text", HostCommand: true, Help: "Optional file used to create missing project containers."},
		{Key: "bin", Label: "Binary", Kind: "text", HostCommand: true, Placeholder: DefaultComposeBin, Help: "Docker binary used with a Compose file."},
	}}
}

func (CommandType) Describe() DriverDescriptor {
	return DriverDescriptor{Type: DriverCommand, Label: "Command", Help: "Run explicit start, stop and status argument lists on the host.", Fields: []typedfields.Field{
		{Key: "start", Label: "Start", Kind: "list", Required: true, HostCommand: true},
		{Key: "stop", Label: "Stop", Kind: "list", Required: true, HostCommand: true},
		{Key: "status", Label: "Status", Kind: "list", HostCommand: true, Help: "A zero exit status means running."},
		{Key: "dir", Label: "Working dir", Kind: "text"},
		{Key: "env", Label: "Environment", Kind: "keyvalue", Sensitive: true},
	}}
}

func (d DockerType) Validate(config json.RawMessage) error {
	if err := typedfields.Validate(d.Describe().Fields, config); err != nil {
		return err
	}
	var spec DockerSpec
	if err := json.Unmarshal(config, &spec); err != nil {
		return err
	}
	if len(spec.Containers) == 0 && strings.TrimSpace(spec.Label) == "" {
		return errors.New("docker services need container names or a label")
	}
	for _, container := range spec.Containers {
		if !containerNamePattern.MatchString(container) {
			return errors.New("invalid container name")
		}
	}
	if strings.ContainsRune(spec.Label, 0) {
		return errors.New("label must not contain NUL")
	}
	return nil
}

func (d ComposeType) Validate(config json.RawMessage) error {
	if err := typedfields.Validate(d.Describe().Fields, config); err != nil {
		return err
	}
	var spec ComposeSpec
	if err := json.Unmarshal(config, &spec); err != nil {
		return err
	}
	for _, value := range append([]string{spec.Project, spec.File, spec.Bin}, spec.Services...) {
		if strings.ContainsRune(value, 0) {
			return errors.New("compose fields must not contain NUL")
		}
	}
	for _, service := range spec.Services {
		if strings.TrimSpace(service) == "" {
			return errors.New("compose services must not be empty")
		}
	}
	return nil
}

func (d CommandType) Validate(config json.RawMessage) error {
	if err := typedfields.Validate(d.Describe().Fields, config); err != nil {
		return err
	}
	var spec ExecSpec
	if err := json.Unmarshal(config, &spec); err != nil {
		return err
	}
	for _, argv := range [][]string{spec.Start, spec.Stop, spec.Status} {
		if len(argv) > 0 && strings.TrimSpace(argv[0]) == "" {
			return errors.New("command arguments must start with a program")
		}
		for _, arg := range argv {
			if strings.ContainsRune(arg, 0) {
				return errors.New("command arguments must not contain NUL")
			}
		}
	}
	if strings.ContainsRune(spec.Dir, 0) {
		return errors.New("working directory must not contain NUL")
	}
	for key, value := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return errors.New("invalid environment entry")
		}
	}
	return nil
}

func (d DockerType) New(config json.RawMessage, deps Deps) (Driver, error) {
	if err := d.Validate(config); err != nil {
		return nil, err
	}
	if deps.Docker == nil {
		return nil, errors.New("Docker client resolver is required")
	}
	client, err := deps.Docker()
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("Docker client resolver returned no client")
	}
	var spec DockerSpec
	if err := json.Unmarshal(config, &spec); err != nil {
		return nil, err
	}
	return newDockerDriver(client, spec), nil
}

func (d ComposeType) New(config json.RawMessage, deps Deps) (Driver, error) {
	if err := validateHostConfig(d.Describe(), config, deps); err != nil {
		return nil, err
	}
	if err := d.Validate(config); err != nil {
		return nil, err
	}
	if deps.Docker == nil {
		return nil, errors.New("Docker client resolver is required")
	}
	client, err := deps.Docker()
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("Docker client resolver returned no client")
	}
	var spec ComposeSpec
	if err := json.Unmarshal(config, &spec); err != nil {
		return nil, err
	}
	spec.Project = strings.TrimSpace(spec.Project)
	spec.File = strings.TrimSpace(spec.File)
	spec.Bin = strings.TrimSpace(spec.Bin)
	if spec.File != "" && spec.Bin == "" {
		spec.Bin = DefaultComposeBin
	}
	return newComposeDriver(client, spec), nil
}

func (d CommandType) New(config json.RawMessage, deps Deps) (Driver, error) {
	if err := validateHostConfig(d.Describe(), config, deps); err != nil {
		return nil, err
	}
	if err := d.Validate(config); err != nil {
		return nil, err
	}
	var spec ExecSpec
	if err := json.Unmarshal(config, &spec); err != nil {
		return nil, err
	}
	return newExecDriver(spec), nil
}
