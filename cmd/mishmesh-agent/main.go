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
		if err := httpCmd(args[1:]); err != nil {
			fail(err)
		}
	case "version", "-version", "--version":
		fmt.Println("mishmesh-agent", version)
	default:
		usage()
		os.Exit(2)
	}
}

func httpCmd(args []string) error {
	cfg := config.LoadAgent()
	fs := flag.NewFlagSet("http", flag.ExitOnError)
	gw := fs.String("gateway", cfg.GatewayURL, "gateway URL (ws://host:port)")
	token := fs.String("token", cfg.Token, "agent authtoken")
	subdomain := fs.String("subdomain", "", "request a specific subdomain (implies reserved)")
	reserved := fs.Bool("reserved", false, "reserved (stable) endpoint instead of ephemeral")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) == 0 {
		return errors.New("usage: mishmesh-agent http <port|host:port> [--subdomain x] [--reserved]")
	}
	if *token == "" {
		return errors.New("no token: pass --token or set MISHMESH_TOKEN")
	}

	lifecycle := store.LifecycleEphemeral
	if *reserved || *subdomain != "" {
		lifecycle = store.LifecycleReserved
	}

	spec := agent.EndpointSpec{
		Kind:        store.KindHTTP,
		Lifecycle:   lifecycle,
		Subdomain:   *subdomain,
		LocalTarget: normalizeTarget(positionals[0]),
	}

	log := newLogger(cfg.LogLevel)
	a := agent.New(agent.Options{
		GatewayURL: *gw,
		Token:      *token,
		Log:        log,
		Endpoints:  []agent.EndpointSpec{spec},
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

func normalizeTarget(arg string) string {
	if strings.HasPrefix(arg, ":") {
		return "127.0.0.1" + arg
	}
	if !strings.Contains(arg, ":") {
		return "127.0.0.1:" + arg
	}
	return arg
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mishmesh-agent http <port|host:port> [--subdomain x] [--reserved] [--gateway ws://...] [--token x]")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
