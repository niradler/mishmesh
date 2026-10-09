package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mishmesh/mishmesh/internal/cluster"
	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/connect/sshfwd"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/metrics"
	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/postgres"
	"github.com/mishmesh/mishmesh/internal/store/redis"
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
		case "help", "-h", "-help", "--help":
			usage(os.Stdout)
			return
		case "serve":
			args = args[1:]
		}
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "error: unknown command or flag %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err := serve(); err != nil {
		fail(err)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `mishmesh-server - tunnel platform server

usage:
  mishmesh-server [serve]                        run the server (configured by MISHMESH_* env vars)
  mishmesh-server token create [flags]           create an org (if new), an agent and a one-time token
  mishmesh-server version                        print version
  mishmesh-server help                           print this help

token create flags:
  --org NAME     org to create or reuse (default "default")
  --name NAME    agent name (default "agent")
  --dsn DSN      data store DSN (default $MISHMESH_DATA_DSN or mishmesh.db; postgres:// supported)
`)
}

func serve() error {
	cfg := config.LoadServer()
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	data, err := openDataStore(cfg)
	if err != nil {
		return err
	}
	defer data.Close()

	conns, clusterRT, err := openConnStore(cfg, log)
	if err != nil {
		return err
	}
	defer clusterRT.close()
	if closer, ok := conns.(io.Closer); ok && clusterRT == nil {
		defer closer.Close()
	}
	limiter := newLimiter(clusterRT, log)
	trustedProxies, err := ingress.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return fmt.Errorf("trusted proxies: %w", err)
	}
	proxyGuard, err := proxy.NewGuard(cfg.ProxyAllowLoopback, cfg.ProxyAllowPrivate, cfg.ProxyAllowedCIDRs)
	if err != nil {
		return err
	}
	proxy.Register(context.Background(), data, conns, log, proxyGuard)

	stopPprof, err := startPprof(cfg.PprofAddr, log)
	if err != nil {
		return err
	}
	defer stopPprof()

	var mx *metrics.Metrics
	if cfg.MetricsEnabled {
		mx = metrics.New()
	}

	var tcpIngress *ingress.TCP
	if cfg.IngressEnabled && cfg.TCPEnabled {
		tcpOpts := ingress.TCPOptions{
			Conns:    conns,
			Data:     data,
			Log:      log,
			BindHost: cfg.TCPBindHost,
			PortMin:  cfg.TCPPortMin,
			PortMax:  cfg.TCPPortMax,
			Meter:    mx,
		}
		if clusterRT != nil {
			tcpOpts.Claims = clusterRT.store.PortClaims(cfg.TCPPortMin, cfg.TCPPortMax)
		}
		tcpIngress = ingress.NewTCP(tcpOpts)
		defer tcpIngress.Shutdown()
		if clusterRT != nil {
			if err := tcpIngress.ListenCluster(); err != nil {
				return err
			}
		}
		log.Info("tcp ingress enabled", "bind", cfg.TCPBindHost, "ports", fmt.Sprintf("%d-%d", cfg.TCPPortMin, cfg.TCPPortMax))
	}

	gwOpts := gateway.Options{
		Data:               data,
		Conns:              conns,
		Log:                log,
		BaseDomain:         cfg.BaseDomain,
		PublicScheme:       cfg.PublicScheme,
		DisablePathRouting: !cfg.PathRouting,
		TLSPublicPort:      cfg.TLSPassthroughPublicPort,
		Metrics:            mx,
	}
	if tcpIngress != nil {
		gwOpts.Ports = tcpIngress
	}
	gwOpts.KindUnavailable = unavailableKinds(cfg, tcpIngress != nil)
	gw := gateway.New(gwOpts)
	if swept, err := gw.SweepOrphanedEphemeral(context.Background()); err != nil {
		log.Warn("sweep of orphaned ephemeral endpoints failed", "err", err)
	} else if swept > 0 {
		log.Info("swept orphaned ephemeral endpoints", "count", swept)
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	cp := controlplane.New(data, conns, cfg.APIAuthToken, log)
	cp.SetPublicConfig(cfg.BaseDomain, cfg.PublicScheme)
	cp.SetPathRouting(cfg.PathRouting)
	cp.SetTLSPublicPort(cfg.TLSPassthroughPublicPort)
	cp.SetLimiter(limiter)
	cp.SetTrustedProxies(trustedProxies)
	cp.SetAllowedOrigins(cfg.AllowedOrigins)
	cp.SetProxyGuard(proxyGuard)
	cp.SetMaxOrgsPerUser(cfg.MaxOrgsPerUser)
	cp.SetDefaultQuota(store.Quota{
		MaxAgents:         cfg.QuotaMaxAgents,
		MaxEndpoints:      cfg.QuotaMaxEndpoints,
		MaxBandwidthBytes: cfg.QuotaMaxBandwidthBytes,
	})
	cp.SetReachInEnabled(cfg.ReachInEnabled)
	cp.SetDomainVerification(cfg.DomainVerification, nil)
	cp.ConfigureAuth(controlplane.AuthOptions{
		Enabled:            cfg.AuthEnabled,
		PasswordEnabled:    cfg.AuthPasswordEnabled,
		SignupMode:         cfg.SignupMode,
		CookieSecure:       cfg.PublicScheme == "https",
		SessionTTL:         time.Duration(cfg.SessionTTLHours) * time.Hour,
		GoogleClientID:     cfg.GoogleClientID,
		GoogleClientSecret: cfg.GoogleClientSecret,
		RedirectURL:        cfg.OIDCRedirectURL,
		Issuer:             cfg.OIDCIssuer,
	})
	cp.Register(apiMux)
	if cfg.ReachInEnabled {
		log.Info("reach-in data-plane api enabled")
	}
	if mx != nil {
		apiMux.Handle("GET /metrics", metricsAuth(cfg.MetricsToken, cfg.APIAuthToken, mx.Handler()))
		log.Info("metrics enabled", "path", "/metrics")
	}
	if cfg.WebUIEnabled && cfg.WebUIDir != "" {
		apiMux.Handle("/", spaHandler(cfg.WebUIDir))
		log.Info("web ui enabled", "dir", cfg.WebUIDir)
	}

	if cfg.BootstrapToken != "" {
		if _, err := cp.EnsureBootstrap(context.Background(), cfg.BootstrapToken); err != nil {
			return fmt.Errorf("bootstrap token: %w", err)
		}
		log.Info("bootstrap token seeded", "agent_id", "ag_bootstrap")
	}

	servers := []*http.Server{newHTTPServer(cfg.APIAddr, apiMux, nil)}
	log.Info("api listener", "addr", cfg.APIAddr)

	if cfg.IngressEnabled {
		ing := ingress.New(ingress.Options{
			Data:                    data,
			Conns:                   conns,
			Log:                     log,
			BaseDomain:              cfg.BaseDomain,
			Meter:                   mx,
			OIDCSignKey:             endpointOIDCKey(cfg),
			CookieSecure:            cfg.PublicScheme == "https",
			OIDCAllowPrivate:        cfg.OIDCAllowPrivate,
			TrustedProxies:          trustedProxies,
			Limiter:                 limiter,
			LookupCacheTTL:          cfg.IngressCacheTTL,
			DisablePathRouting:      !cfg.PathRouting,
			UpstreamResponseTimeout: cfg.UpstreamResponseTimeout,
		})
		if cfg.TLSEnabled {
			tc, acmeHTTP, err := buildTLSConfig(cfg, data)
			if err != nil {
				return err
			}
			servers = append(servers, newIngressHTTPServer(cfg.HTTPSAddr, ing, tc))
			log.Info("ingress https listener", "addr", cfg.HTTPSAddr, "base_domain", cfg.BaseDomain)
			httpHandler := http.Handler(ing)
			if acmeHTTP != nil {
				httpHandler = acmeHTTP
			}
			servers = append(servers, newIngressHTTPServer(cfg.IngressAddr, httpHandler, nil))
			log.Info("ingress http listener", "addr", cfg.IngressAddr)
		} else {
			servers = append(servers, newIngressHTTPServer(cfg.IngressAddr, ing, nil))
			log.Info("ingress listener", "addr", cfg.IngressAddr, "base_domain", cfg.BaseDomain)
		}
		if cfg.TLSPassthroughEnabled {
			tp := ingress.NewTLSPassthrough(ingress.TLSPassthroughOptions{Data: data, Conns: conns, Log: log, BaseDomain: cfg.BaseDomain, Meter: mx})
			if err := tp.Listen(cfg.TLSPassthroughAddr); err != nil {
				return err
			}
			defer tp.Shutdown()
			log.Info("tls passthrough listener", "addr", cfg.TLSPassthroughAddr)
		}
	}

	if cfg.SSHEnabled {
		sshOpts := sshfwd.Options{
			Data:               data,
			Conns:              conns,
			Log:                log,
			BaseDomain:         cfg.BaseDomain,
			PublicScheme:       cfg.PublicScheme,
			DisablePathRouting: !cfg.PathRouting,
		}
		if tcpIngress != nil {
			sshOpts.Ports = tcpIngress
		}
		if mx != nil {
			sshOpts.Metrics = mx
		}
		keyPath := cfg.SSHHostKeyFile
		if keyPath == "" {
			keyPath = defaultHostKeyPath(cfg)
		}
		keyPEM, err := loadOrCreateHostKey(keyPath)
		if err != nil {
			return fmt.Errorf("ssh host key: %w", err)
		}
		sshOpts.HostKeyPEM = keyPEM
		log.Info("ssh host key", "path", keyPath)
		sshSrv, err := sshfwd.New(sshOpts)
		if err != nil {
			return err
		}
		if _, err := sshSrv.Listen(cfg.SSHAddr); err != nil {
			return err
		}
		defer sshSrv.Shutdown()
		log.Info("clientless ssh remote-forward listener", "addr", cfg.SSHAddr)
	}

	hooks := shutdownHooks{
		onDrainStart: func() {
			cp.SetDraining(true)
			if tcpIngress != nil {
				tcpIngress.Shutdown()
			}
		},
		onServersStopped: clusterRT.drain,
	}
	return runServers(log, servers, hooks)
}

func openDataStore(cfg config.Server) (store.DataStore, error) {
	backend := cfg.EffectiveDataBackend()
	switch backend {
	case "postgres":
		return postgres.Open(cfg.DataDSN, postgres.PoolConfig{
			MaxOpenConns:    cfg.DataMaxConns,
			MaxIdleConns:    cfg.DataMaxIdleConns,
			ConnMaxLifetime: cfg.DataConnMaxLifetime,
			ConnMaxIdleTime: cfg.DataConnMaxIdleTime,
			QueryTimeout:    cfg.DataQueryTimeout,
		})
	case "sqlite":
		return sqlite.Open(cfg.DataDSN)
	default:
		return nil, fmt.Errorf("unknown DATA_BACKEND %q (want sqlite or postgres)", backend)
	}
}

type clusterRuntime struct {
	store *redis.ClusterConnStore
	relay *cluster.Server
}

func (c *clusterRuntime) drain(ctx context.Context) {
	if c == nil {
		return
	}
	c.store.Drain(ctx)
	c.relay.Shutdown()
}

func newLimiter(c *clusterRuntime, log *slog.Logger) ratelimit.Limiter {
	if c == nil {
		return ratelimit.NewMemory()
	}
	log.Info("rate limits are cluster-wide (redis)")
	return c.store.Limiter(log)
}

func (c *clusterRuntime) close() {
	if c == nil {
		return
	}
	_ = c.store.Close()
}

func openConnStore(cfg config.Server, log *slog.Logger) (store.ConnectionStore, *clusterRuntime, error) {
	switch cfg.ConnBackend {
	case "redis":
		if cfg.RedisURL == "" {
			return nil, nil, fmt.Errorf("CONN_BACKEND=redis requires REDIS_URL")
		}
		if cfg.ClusterEnabled {
			return openClusterConnStore(cfg, log)
		}
		cs, err := redis.NewConnStore(cfg.RedisURL, cfg.RedisPoolSize)
		if err != nil {
			return nil, nil, err
		}
		log.Info("redis connection store enabled")
		return cs, nil, nil
	case "", "memory":
		return memory.NewConnStore(), nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown CONN_BACKEND %q (want memory or redis)", cfg.ConnBackend)
	}
}

func openClusterConnStore(cfg config.Server, log *slog.Logger) (store.ConnectionStore, *clusterRuntime, error) {
	secret := []byte(cfg.ClusterSecret)
	cs, err := redis.NewClusterConnStore(context.Background(), cfg.RedisURL, redis.ClusterOptions{
		NodeID:    cfg.NodeID,
		Advertise: cfg.RelayAdvertise,
		Relay:     cluster.NewClient(secret),
		PoolSize:  cfg.RedisPoolSize,
		Log:       log,
	})
	if err != nil {
		return nil, nil, err
	}
	relay := cluster.NewServer(cluster.ServerOptions{Secret: secret, Local: cs, Log: log})
	if _, err := relay.Listen(cfg.RelayAddr); err != nil {
		_ = cs.Close()
		return nil, nil, fmt.Errorf("relay listen %s: %w", cfg.RelayAddr, err)
	}
	log.Info("cluster mode enabled", "node_id", cfg.NodeID, "relay_addr", cfg.RelayAddr, "relay_advertise", cfg.RelayAdvertise)
	return cs, &clusterRuntime{store: cs, relay: relay}, nil
}

func spaHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if !strings.HasPrefix(clean, filepath.Clean(dir)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if st, err := os.Stat(clean); err == nil && !st.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
}

type shutdownHooks struct {
	onDrainStart     func()
	onServersStopped func(ctx context.Context)
}

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverIdleTimeout       = 120 * time.Second
)

func newHTTPServer(addr string, handler http.Handler, tc *tls.Config) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		TLSConfig:         tc,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}

func newIngressHTTPServer(addr string, handler http.Handler, config *tls.Config) *http.Server {
	server := newHTTPServer(addr, handler, config)
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	server.Protocols = protocols
	return server
}

const (
	shutdownTimeout = 10 * time.Second
	drainTimeout    = 5 * time.Second
)

func runServers(log *slog.Logger, servers []*http.Server, hooks shutdownHooks) error {
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

	if hooks.onDrainStart != nil {
		hooks.onDrainStart()
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			_ = s.Shutdown(shutCtx)
		}(srv)
	}
	wg.Wait()
	if hooks.onServersStopped != nil {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), drainTimeout)
		defer drainCancel()
		hooks.onServersStopped(drainCtx)
	}
	log.Info("shutdown complete")
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

func endpointOIDCKey(cfg config.Server) []byte {
	if cfg.EndpointOIDCKey != "" {
		sum := sha256.Sum256([]byte(cfg.EndpointOIDCKey))
		return sum[:]
	}
	if cfg.APIAuthToken != "" {
		sum := sha256.Sum256([]byte("endpoint-oidc:" + cfg.APIAuthToken))
		return sum[:]
	}
	return nil
}

func unavailableKinds(cfg config.Server, tcpActive bool) map[string]string {
	out := make(map[string]string)
	if !cfg.IngressEnabled {
		const reason = "ingress is disabled on this server (MISHMESH_INGRESS_ENABLED=false)"
		out[store.KindHTTP] = "http " + reason
		out[store.KindTLS] = "tls " + reason
		out[store.KindTCP] = "tcp " + reason
		return out
	}
	if !cfg.TLSPassthroughEnabled {
		out[store.KindTLS] = "tls passthrough is disabled on this server (MISHMESH_TLS_PASSTHROUGH_ENABLED=false)"
	}
	if !tcpActive {
		out[store.KindTCP] = "tcp ingress is disabled on this server (MISHMESH_TCP_ENABLED=false)"
	}
	return out
}
