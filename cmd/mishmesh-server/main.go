package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "token":
			if err := tokenCmd(args[1:]); err != nil {
				fail(err)
			}
			return
		case "version", "-version", "--version":
			fmt.Println("mishmesh-server", version)
			return
		case "serve":
			args = args[1:]
		}
	}
	if err := serve(args); err != nil {
		fail(err)
	}
}

func serve(_ []string) error {
	cfg := config.LoadServer()
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	data, err := sqlite.Open(cfg.DataDSN)
	if err != nil {
		return err
	}
	defer data.Close()

	conns := memory.NewConnStore()

	var tcpIngress *ingress.TCP
	if cfg.IngressEnabled && cfg.TCPEnabled {
		tcpIngress = ingress.NewTCP(ingress.TCPOptions{
			Conns:    conns,
			Log:      log,
			BindHost: cfg.TCPBindHost,
			PortMin:  cfg.TCPPortMin,
			PortMax:  cfg.TCPPortMax,
		})
		defer tcpIngress.Shutdown()
		log.Info("tcp ingress enabled", "bind", cfg.TCPBindHost, "ports", fmt.Sprintf("%d-%d", cfg.TCPPortMin, cfg.TCPPortMax))
	}

	gwOpts := gateway.Options{
		Data:         data,
		Conns:        conns,
		Log:          log,
		BaseDomain:   cfg.BaseDomain,
		PublicScheme: cfg.PublicScheme,
	}
	if tcpIngress != nil {
		gwOpts.Ports = tcpIngress
	}
	gw := gateway.New(gwOpts)

	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	controlplane.New(data, conns, log).Register(apiMux)

	servers := []*http.Server{{Addr: cfg.APIAddr, Handler: apiMux}}
	log.Info("api listener", "addr", cfg.APIAddr)

	if cfg.IngressEnabled {
		ing := ingress.New(ingress.Options{Data: data, Conns: conns, Log: log, BaseDomain: cfg.BaseDomain})
		if cfg.TLSEnabled {
			tc, acmeHTTP, err := buildTLSConfig(cfg)
			if err != nil {
				return err
			}
			servers = append(servers, &http.Server{Addr: cfg.HTTPSAddr, Handler: ing, TLSConfig: tc})
			log.Info("ingress https listener", "addr", cfg.HTTPSAddr, "base_domain", cfg.BaseDomain)
			httpHandler := http.Handler(ing)
			if acmeHTTP != nil {
				httpHandler = acmeHTTP
			}
			servers = append(servers, &http.Server{Addr: cfg.IngressAddr, Handler: httpHandler})
			log.Info("ingress http listener", "addr", cfg.IngressAddr)
		} else {
			servers = append(servers, &http.Server{Addr: cfg.IngressAddr, Handler: ing})
			log.Info("ingress listener", "addr", cfg.IngressAddr, "base_domain", cfg.BaseDomain)
		}
	}
	if cfg.WebUIEnabled {
		log.Warn("MISHMESH_WEBUI_ENABLED is set but the web UI is not built in this MVP")
	}

	return runServers(log, servers)
}

func runServers(log *slog.Logger, servers []*http.Server) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func(s *http.Server) {
			var err error
			if s.TLSConfig != nil {
				err = s.ListenAndServeTLS("", "")
			} else {
				err = s.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("listen %s: %w", s.Addr, err)
			}
		}(srv)
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		return err
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutCtx)
	}
	return nil
}

func tokenCmd(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	dsn := fs.String("dsn", envOr("MISHMESH_DATA_DSN", "mishmesh.db"), "DataStore DSN")
	orgName := fs.String("org", "default", "org name")
	agentName := fs.String("name", "agent", "agent name")
	_ = fs.Parse(args)

	if len(fs.Args()) == 0 || fs.Arg(0) != "create" {
		return fmt.Errorf("usage: mishmesh-server token create [--dsn x] [--org x] [--name x]")
	}

	data, err := sqlite.Open(*dsn)
	if err != nil {
		return err
	}
	defer data.Close()

	ctx := context.Background()
	now := time.Now()
	org := &store.Org{ID: store.NewID("org"), Name: *orgName, CreatedAt: now}
	if err := data.CreateOrg(ctx, org); err != nil {
		return err
	}
	agent := &store.Agent{ID: store.NewID("ag"), OrgID: org.ID, Name: *agentName, Status: store.AgentActive, CreatedAt: now}
	if err := data.CreateAgent(ctx, agent); err != nil {
		return err
	}
	raw, hash, err := store.GenerateToken()
	if err != nil {
		return err
	}
	tok := &store.Token{ID: store.NewID("tok"), OrgID: org.ID, AgentID: agent.ID, Hash: hash, CreatedAt: now}
	if err := data.CreateToken(ctx, tok); err != nil {
		return err
	}

	fmt.Printf("org_id:   %s\n", org.ID)
	fmt.Printf("agent_id: %s\n", agent.ID)
	fmt.Printf("token:    %s\n", raw)
	fmt.Println("\nrun the agent with:")
	fmt.Printf("  MISHMESH_TOKEN=%s mishmesh-agent http 3000\n", raw)
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
