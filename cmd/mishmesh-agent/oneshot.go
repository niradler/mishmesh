package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/store"
)

func oneShotCmd(kind string, args []string) error {
	cfg := config.LoadAgent()
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	gw := fs.String("gateway", cfg.GatewayURL, "gateway URL (ws://host:port)")
	token := fs.String("token", cfg.Token, "agent authtoken")
	subdomain := fs.String("subdomain", "", "request a specific subdomain (http/tls; implies reserved)")
	domain := fs.String("domain", "", "pre-registered custom domain (http/tls)")
	port := fs.Int("port", 0, "request a specific public port (tcp only; implies reserved)")
	reserved := fs.Bool("reserved", false, "reserved (stable) endpoint instead of ephemeral")
	targetHTTPS := fs.Bool("target-https", false, "local target speaks TLS (dial it over https)")
	insecure := fs.Bool("insecure", false, "skip TLS verification of the local target (self-signed)")
	allow := fs.String("allow", cfg.Allow, "reach-in allowlist rules, comma-separated host|cidr[:port;port] (deny-first)")

	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: mishmesh-agent %s <port|host:port> [flags]", kind)
	}
	if *token == "" {
		return errors.New("no token: pass --token or set MISHMESH_TOKEN")
	}
	if kind == store.KindTLS && *subdomain == "" && *domain == "" {
		return errors.New("tls tunnels need --subdomain or --domain")
	}

	lifecycle := store.LifecycleEphemeral
	if *reserved || *subdomain != "" || *domain != "" || *port != 0 {
		lifecycle = store.LifecycleReserved
	}
	addr, schemeTLS := agent.NormalizeTarget(positionals[0])
	spec := agent.EndpointSpec{
		Name:           kind,
		Kind:           kind,
		Lifecycle:      lifecycle,
		Subdomain:      *subdomain,
		Domain:         *domain,
		Port:           *port,
		LocalTarget:    addr,
		TargetTLS:      (*targetHTTPS || schemeTLS) && kind != store.KindTLS,
		TargetInsecure: *insecure,
	}

	ctx, stop := signalContext()
	defer stop()
	return runAgent(ctx, agent.Options{
		GatewayURL: *gw,
		Token:      *token,
		Log:        newLogger(cfg.LogLevel),
		Out:        os.Stdout,
		Endpoints:  []agent.EndpointSpec{spec},
		Allowlist:  splitCSV(*allow),
	})
}
