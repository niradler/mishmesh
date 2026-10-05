package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/kardianos/service"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/config"
)

func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return agent.FindConfig(agent.DefaultConfigPaths())
}

func buildStartOptions(configPath, gateway, token, allow string, names []string) (agent.Options, error) {
	path, err := resolveConfigPath(configPath)
	if err != nil {
		return agent.Options{}, err
	}
	file, err := agent.LoadConfigFile(path, os.LookupEnv)
	if err != nil {
		return agent.Options{}, err
	}
	specs, err := file.Specs(names)
	if err != nil {
		return agent.Options{}, err
	}
	env := config.LoadAgent()
	opts := agent.Options{
		GatewayURL: firstNonEmpty(gateway, file.Gateway, env.GatewayURL),
		Token:      firstNonEmpty(token, file.Token, env.Token),
		Log:        newLogger(firstNonEmpty(file.LogLevel, env.LogLevel)),
		Out:        os.Stdout,
		Endpoints:  specs,
		Allowlist:  file.Allow,
	}
	if allow != "" {
		opts.Allowlist = splitCSV(allow)
	} else if len(opts.Allowlist) == 0 {
		opts.Allowlist = splitCSV(env.Allow)
	}
	if opts.Token == "" {
		return agent.Options{}, errors.New("no token: set token in the config file, pass --token, or set MISHMESH_TOKEN")
	}
	return opts, nil
}

func lenientLookup(name string) (string, bool) {
	if v, ok := os.LookupEnv(name); ok {
		return v, true
	}
	return "unset", true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func startCmd(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file (default: search ./mishmesh.yml, ~/.config/mishmesh/agent.yml, /etc/mishmesh/agent.yml)")
	gateway := fs.String("gateway", "", "gateway URL, overrides the config file")
	token := fs.String("token", "", "agent authtoken, overrides the config file")
	allow := fs.String("allow", "", "reach-in allowlist rules, overrides the config file")
	names, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	opts, err := buildStartOptions(*configPath, *gateway, *token, *allow, names)
	if err != nil {
		return err
	}
	return runManaged(opts)
}

func validateCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	path, err := resolveConfigPath(*configPath)
	if err != nil {
		return err
	}
	file, err := agent.LoadConfigFile(path, lenientLookup)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: ok, %d tunnels: %v\n", path, len(file.Tunnels), file.TunnelNames())
	return nil
}

func runManaged(opts agent.Options) error {
	prg := &program{opts: opts}
	svc, err := service.New(prg, &service.Config{Name: serviceName})
	if err != nil || service.Interactive() {
		ctx, stop := signalContext()
		defer stop()
		return runAgent(ctx, opts)
	}
	prg.log = opts.Log
	return svc.Run()
}

type program struct {
	opts   agent.Options
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if err := runAgent(ctx, p.opts); err != nil {
			p.log.Error("agent stopped", "err", err)
			os.Exit(1)
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	p.once.Do(func() {
		p.cancel()
		<-p.done
	})
	return nil
}
