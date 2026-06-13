package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/store"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "http":
		if err := runEndpoint("http", args[1:]); err != nil {
			fail(err)
		}
	case "tcp":
		if err := runEndpoint("tcp", args[1:]); err != nil {
			fail(err)
		}
	case "version", "-version", "--version":
		fmt.Println("mishmesh-agent", version)
	default:
		usage()
		os.Exit(2)
	}
}

func runEndpoint(kind string, args []string) error {
	cfg := config.LoadAgent()
	fs := flag.NewFlagSet(kind, flag.ExitOnError)
	gw := fs.String("gateway", cfg.GatewayURL, "gateway URL (ws://host:port)")
	token := fs.String("token", cfg.Token, "agent authtoken")
	subdomain := fs.String("subdomain", "", "request a specific subdomain (http only; implies reserved)")
	port := fs.Int("port", 0, "request a specific public port (tcp only; implies reserved)")
	reserved := fs.Bool("reserved", false, "reserved (stable) endpoint instead of ephemeral")
	targetHTTPS := fs.Bool("target-https", false, "local target speaks TLS (dial it over https)")
	insecure := fs.Bool("insecure", false, "skip TLS verification of the local target (self-signed)")
	allow := fs.String("allow", cfg.Allow, "reach-in allowlist rules, comma-separated host|cidr[:port;port] (deny-first)")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) == 0 {
		return fmt.Errorf("usage: mishmesh-agent %s <port|host:port> [--subdomain x | --port N] [--reserved]", kind)
	}
	if *token == "" {
		return errors.New("no token: pass --token or set MISHMESH_TOKEN")
	}

	lifecycle := store.LifecycleEphemeral
	if *reserved || *subdomain != "" || *port != 0 {
		lifecycle = store.LifecycleReserved
	}

	addr, schemeTLS := normalizeTarget(positionals[0])
	spec := agent.EndpointSpec{
		Kind:           kind,
		Lifecycle:      lifecycle,
		Subdomain:      *subdomain,
		Port:           *port,
		LocalTarget:    addr,
		TargetTLS:      *targetHTTPS || schemeTLS,
		TargetInsecure: *insecure,
	}

	log := newLogger(cfg.LogLevel)
	a := agent.New(agent.Options{
		GatewayURL: *gw,
		Token:      *token,
		Log:        log,
		Endpoints:  []agent.EndpointSpec{spec},
		Allowlist:  splitCSV(*allow),
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("mishmesh tunnel -> %s\n", spec.LocalTarget)
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

func normalizeTarget(arg string) (addr string, useTLS bool) {
	switch {
	case strings.HasPrefix(arg, "https://"):
		arg = strings.TrimPrefix(arg, "https://")
		useTLS = true
	case strings.HasPrefix(arg, "tls://"):
		arg = strings.TrimPrefix(arg, "tls://")
		useTLS = true
	case strings.HasPrefix(arg, "http://"):
		arg = strings.TrimPrefix(arg, "http://")
	}
	arg = strings.TrimSuffix(arg, "/")
	if strings.HasPrefix(arg, ":") {
		return "127.0.0.1" + arg, useTLS
	}
	if !strings.Contains(arg, ":") {
		return "127.0.0.1:" + arg, useTLS
	}
	return arg, useTLS
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  mishmesh-agent http <port|host:port> [--subdomain x] [--reserved] [--gateway ws://...] [--token x]")
	fmt.Fprintln(os.Stderr, "  mishmesh-agent tcp  <port|host:port> [--port N] [--reserved] [--gateway ws://...] [--token x]")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
