package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
)

type commandRunner func(ctx context.Context, argv []string, dir string, env map[string]string) (string, error)

type containerSelector struct {
	client   *dockerClient
	filters  []map[string][]string
	expected int
}

func (s containerSelector) list(ctx context.Context) ([]dockerContainer, error) {
	seen := make(map[string]struct{})
	var all []dockerContainer
	for _, filter := range s.filters {
		found, err := s.client.listContainers(ctx, filter)
		if err != nil {
			return nil, err
		}
		for _, c := range found {
			if _, dup := seen[c.ID]; dup {
				continue
			}
			seen[c.ID] = struct{}{}
			all = append(all, c)
		}
	}
	return all, nil
}

func (s containerSelector) state(ctx context.Context) (State, error) {
	containers, err := s.list(ctx)
	if err != nil {
		return Unknown, err
	}
	running := 0
	for _, c := range containers {
		if c.running() {
			running++
		}
	}
	switch {
	case running == 0:
		return Stopped, nil
	case running == len(containers) && len(containers) >= s.expected:
		return Running, nil
	default:
		return Partial, nil
	}
}

func (s containerSelector) startAll(ctx context.Context, containers []dockerContainer) error {
	var errs []error
	for _, c := range containers {
		if c.running() {
			continue
		}
		if err := s.client.startContainer(ctx, c.ID); err != nil {
			errs = append(errs, fmt.Errorf("container %s: %w", c.displayName(), err))
		}
	}
	return errors.Join(errs...)
}

func (s containerSelector) stopAll(ctx context.Context) error {
	containers, err := s.list(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range containers {
		if !c.running() {
			continue
		}
		if err := s.client.stopContainer(ctx, c.ID); err != nil {
			errs = append(errs, fmt.Errorf("container %s: %w", c.displayName(), err))
		}
	}
	return errors.Join(errs...)
}

type dockerDriver struct {
	selector containerSelector
}

func newDockerDriver(client *dockerClient, spec DockerSpec) *dockerDriver {
	selector := containerSelector{client: client}
	if len(spec.Containers) > 0 {
		patterns := make([]string, len(spec.Containers))
		for i, name := range spec.Containers {
			patterns[i] = "^/" + regexp.QuoteMeta(name) + "$"
		}
		selector.filters = []map[string][]string{{"name": patterns}}
		selector.expected = len(spec.Containers)
	} else {
		selector.filters = []map[string][]string{{"label": {spec.Label}}}
	}
	return &dockerDriver{selector: selector}
}

func (d *dockerDriver) Start(ctx context.Context) error {
	containers, err := d.selector.list(ctx)
	if err != nil {
		return err
	}
	if len(containers) == 0 || len(containers) < d.selector.expected {
		return fmt.Errorf("found %d of %d expected containers", len(containers), max(d.selector.expected, 1))
	}
	return d.selector.startAll(ctx, containers)
}

func (d *dockerDriver) Stop(ctx context.Context) error {
	return d.selector.stopAll(ctx)
}

func (d *dockerDriver) State(ctx context.Context) (State, error) {
	return d.selector.state(ctx)
}

type composeDriver struct {
	selector containerSelector
	spec     ComposeSpec
	run      commandRunner
}

func newComposeDriver(client *dockerClient, spec ComposeSpec) *composeDriver {
	projectFilter := composeProjectLabel + "=" + spec.Project
	selector := containerSelector{client: client}
	if len(spec.Services) == 0 {
		selector.filters = []map[string][]string{{"label": {projectFilter}}}
	} else {
		for _, service := range spec.Services {
			selector.filters = append(selector.filters, map[string][]string{
				"label": {projectFilter, composeServiceLabel + "=" + service},
			})
		}
	}
	return &composeDriver{selector: selector, spec: spec, run: runCommand}
}

func (d *composeDriver) Start(ctx context.Context) error {
	containers, err := d.selector.list(ctx)
	if err != nil {
		return err
	}
	if len(containers) > 0 {
		return d.selector.startAll(ctx, containers)
	}
	if d.spec.File == "" {
		return fmt.Errorf("no containers for compose project %q and no file configured to create them", d.spec.Project)
	}
	argv := []string{d.spec.Bin, "compose", "-f", d.spec.File, "-p", d.spec.Project, "up", "-d"}
	argv = append(argv, d.spec.Services...)
	if _, err := d.run(ctx, argv, "", nil); err != nil {
		return fmt.Errorf("compose up: %w", err)
	}
	return nil
}

func (d *composeDriver) Stop(ctx context.Context) error {
	return d.selector.stopAll(ctx)
}

func (d *composeDriver) State(ctx context.Context) (State, error) {
	return d.selector.state(ctx)
}
