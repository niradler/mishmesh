package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kardianos/service"

	"github.com/mishmesh/mishmesh/internal/agent"
)

const serviceName = "mishmesh-agent"

var serviceActions = []string{"install", "uninstall", "start", "stop", "status"}

func serviceConfig(execPath, configPath, user string) *service.Config {
	return &service.Config{
		Name:        serviceName,
		DisplayName: "mishmesh agent",
		Description: "mishmesh tunnel agent",
		Executable:  execPath,
		Arguments:   []string{"start", "--config", configPath},
		UserName:    user,
		Option: service.KeyValue{
			"Restart":    "on-failure",
			"RestartSec": 10,
			"RunAtLoad":  true,
			"KeepAlive":  true,
		},
	}
}

func serviceCmd(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mishmesh-agent service %v [--config path] [--user u] [--dry-run]", serviceActions)
	}
	action := args[0]
	known := false
	for _, a := range serviceActions {
		known = known || a == action
	}
	if !known {
		return fmt.Errorf("unknown service action %q (want one of %v)", action, serviceActions)
	}

	fs := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	configPath := fs.String("config", "", "config file the service runs with (install only)")
	user := fs.String("user", "", "account the service runs as (install only)")
	dryRun := fs.Bool("dry-run", false, "print the service definition without touching the system")
	if _, err := parseInterspersed(fs, args[1:]); err != nil {
		return err
	}

	cfg := serviceConfig("", "", *user)
	if action == "install" {
		var err error
		if cfg, err = installConfig(*configPath, *user); err != nil {
			return err
		}
	}
	if *dryRun {
		printServiceConfig(out, action, cfg)
		return nil
	}

	svc, err := service.New(&program{}, cfg)
	if err != nil {
		return fmt.Errorf("service: %w", err)
	}
	if action == "status" {
		return printStatus(out, svc)
	}
	if err := service.Control(svc, action); err != nil {
		return fmt.Errorf("service %s: %w", action, err)
	}
	fmt.Fprintf(out, "service %s: ok (%s)\n", action, svc.Platform())
	return nil
}

func installConfig(configPath, user string) (*service.Config, error) {
	path, err := resolveConfigPath(configPath)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	if _, err := agent.LoadConfigFile(abs, os.LookupEnv); err != nil {
		return nil, err
	}
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	return serviceConfig(execPath, abs, user), nil
}

func printServiceConfig(out io.Writer, action string, cfg *service.Config) {
	fmt.Fprintf(out, "dry run: service %s\n", action)
	fmt.Fprintf(out, "  name:       %s\n", cfg.Name)
	fmt.Fprintf(out, "  executable: %s\n", cfg.Executable)
	fmt.Fprintf(out, "  arguments:  %v\n", cfg.Arguments)
	if cfg.UserName != "" {
		fmt.Fprintf(out, "  user:       %s\n", cfg.UserName)
	}
}

func printStatus(out io.Writer, svc service.Service) error {
	st, err := svc.Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		fmt.Fprintln(out, "not installed")
		return nil
	case err != nil:
		return fmt.Errorf("service status: %w", err)
	}
	switch st {
	case service.StatusRunning:
		fmt.Fprintln(out, "running")
	case service.StatusStopped:
		fmt.Fprintln(out, "stopped")
	default:
		fmt.Fprintln(out, "unknown")
	}
	return nil
}
