package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mishmesh/mishmesh/internal/agent"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "start":
		err = startCmd(args[1:])
	case "validate":
		err = validateCmd(args[1:], os.Stdout)
	case "http", "tcp", "tls":
		err = oneShotCmd(args[0], args[1:])
	case "service":
		err = serviceCmd(args[1:], os.Stdout)
	case "version", "-version", "--version":
		fmt.Println("mishmesh-agent", version)
	case "help", "-h", "-help", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fail(err)
	}
}

func runAgent(ctx context.Context, opts agent.Options) error {
	err := agent.New(opts).Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  mishmesh-agent start [name ...] [--config path] [--gateway url] [--token t] [--allow rules]
  mishmesh-agent validate [--config path]
  mishmesh-agent http <port|host:port> [--subdomain x] [--reserved] [--target-https] [--insecure]
  mishmesh-agent tcp  <port|host:port> [--port N] [--reserved]
  mishmesh-agent tls  <port|host:port> --subdomain x | --domain d
  mishmesh-agent service install|uninstall|start|stop|status [--config path] [--user u] [--dry-run]
  mishmesh-agent version

config search order: ./mishmesh.yml, ~/.config/mishmesh/agent.yml, /etc/mishmesh/agent.yml
environment: MISHMESH_GATEWAY_URL, MISHMESH_TOKEN, MISHMESH_LOG_LEVEL, MISHMESH_ALLOW
`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
