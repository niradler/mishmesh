package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/connect/sshfwd"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/metrics"
	"github.com/mishmesh/mishmesh/internal/orgsecret"
	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/store"
)

type cleanupStack []func()

func (c *cleanupStack) add(fn func()) {
	*c = append(*c, fn)
}

func (c *cleanupStack) run() {
	for i := len(*c) - 1; i >= 0; i-- {
		(*c)[i]()
	}
}

type serverEnv struct {
	cfg       config.Server
	log       *slog.Logger
	data      store.DataStore
	conns     store.ConnectionStore
	clusterRT *clusterRuntime
	mx        *metrics.Metrics
	cleanup   *cleanupStack
}

func (e *serverEnv) startTCPIngress() (*ingress.TCP, error) {
	if !e.cfg.IngressEnabled || !e.cfg.TCPEnabled {
		return nil, nil
	}
	opts := ingress.TCPOptions{
		Conns:    e.conns,
		Data:     e.data,
		Log:      e.log,
		BindHost: e.cfg.TCPBindHost,
		PortMin:  e.cfg.TCPPortMin,
		PortMax:  e.cfg.TCPPortMax,
		Meter:    e.mx,
	}
	if e.clusterRT != nil {
		opts.Claims = e.clusterRT.store.PortClaims(e.cfg.TCPPortMin, e.cfg.TCPPortMax)
	}
	tcp := ingress.NewTCP(opts)
	e.cleanup.add(tcp.Shutdown)
	if e.clusterRT != nil {
		if err := tcp.ListenCluster(); err != nil {
			return nil, err
		}
	}
	e.log.Info("tcp ingress enabled", "bind", e.cfg.TCPBindHost, "ports", fmt.Sprintf("%d-%d", e.cfg.TCPPortMin, e.cfg.TCPPortMax))
	return tcp, nil
}

func (e *serverEnv) startUDPIngress() *ingress.UDP {
	if !e.cfg.IngressEnabled {
		return nil
	}
	if e.clusterRT != nil {
		e.log.Warn("udp ingress is not supported in cluster mode yet; public udp ports are disabled")
		return nil
	}
	udp := ingress.NewUDP(ingress.UDPOptions{
		Conns:    e.conns,
		Data:     e.data,
		Log:      e.log,
		BindHost: e.cfg.UDPBindHost,
		PortMin:  e.cfg.UDPPortMin,
		PortMax:  e.cfg.UDPPortMax,
		Meter:    e.mx,
		Idle:     e.cfg.UDPIdleTimeout,
		MaxFlows: e.cfg.UDPMaxFlows,
	})
	e.cleanup.add(udp.Shutdown)
	e.log.Info("udp ingress enabled", "bind", e.cfg.UDPBindHost, "ports", fmt.Sprintf("%d-%d", e.cfg.UDPPortMin, e.cfg.UDPPortMax))
	return udp
}

func (e *serverEnv) attachBuses(idSvc *identity.Service, vault *orgsecret.Vault) error {
	bus, ok := e.conns.(store.EventBus)
	if !ok {
		return nil
	}
	stopBus, err := idSvc.AttachBus(bus, e.log)
	if err != nil {
		return err
	}
	e.cleanup.add(stopBus)
	stopSecretBus, err := vault.AttachBus(bus, e.log)
	if err != nil {
		return err
	}
	e.cleanup.add(stopSecretBus)
	e.log.Info("revocations, decision flushes and secret invalidations shared across nodes")
	return nil
}

func configureControlPlane(cp *controlplane.API, cfg config.Server, limiter ratelimit.Limiter, trustedProxies []*net.IPNet, guard *proxy.Guard) {
	cp.SetPublicConfig(cfg.BaseDomain, cfg.PublicScheme)
	cp.SetPathRouting(cfg.PathRouting)
	cp.SetTLSPublicPort(cfg.TLSPassthroughPublicPort)
	cp.SetLimiter(limiter)
	cp.SetTrustedProxies(trustedProxies)
	cp.SetAllowedOrigins(cfg.AllowedOrigins)
	cp.SetProxyGuard(guard)
	cp.SetMaxOrgsPerUser(cfg.MaxOrgsPerUser)
	cp.SetDefaultQuota(store.Quota{
		MaxAgents:         cfg.QuotaMaxAgents,
		MaxEndpoints:      cfg.QuotaMaxEndpoints,
		MaxBandwidthBytes: cfg.QuotaMaxBandwidthBytes,
	})
	cp.SetReachInEnabled(cfg.ReachInEnabled)
	cp.SetAPIPublicURL(cfg.APIPublicURL)
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
}

func mountOptionalAPI(mux *http.ServeMux, cfg config.Server, mx *metrics.Metrics, log *slog.Logger) {
	if cfg.ReachInEnabled {
		log.Info("reach-in data-plane api enabled")
	}
	if mx != nil {
		mux.Handle("GET /metrics", metricsAuth(cfg.MetricsToken, cfg.APIAuthToken, mx.Handler()))
		log.Info("metrics enabled", "path", "/metrics")
	}
	if cfg.WebUIEnabled && cfg.WebUIDir != "" {
		mux.Handle("/", spaHandler(cfg.WebUIDir))
		log.Info("web ui enabled", "dir", cfg.WebUIDir)
	}
}

func (e *serverEnv) ingressServers(handler http.Handler) ([]*http.Server, error) {
	cfg := e.cfg
	if !cfg.TLSEnabled {
		e.log.Info("ingress listener", "addr", cfg.IngressAddr, "base_domain", cfg.BaseDomain)
		return []*http.Server{newIngressHTTPServer(cfg.IngressAddr, handler, nil)}, nil
	}
	tc, acmeHTTP, err := buildTLSConfig(cfg, e.data)
	if err != nil {
		return nil, err
	}
	httpHandler := handler
	if acmeHTTP != nil {
		httpHandler = acmeHTTP
	}
	e.log.Info("ingress https listener", "addr", cfg.HTTPSAddr, "base_domain", cfg.BaseDomain)
	e.log.Info("ingress http listener", "addr", cfg.IngressAddr)
	return []*http.Server{
		newIngressHTTPServer(cfg.HTTPSAddr, handler, tc),
		newIngressHTTPServer(cfg.IngressAddr, httpHandler, nil),
	}, nil
}

func (e *serverEnv) startTLSPassthrough() error {
	if !e.cfg.TLSPassthroughEnabled {
		return nil
	}
	tp := ingress.NewTLSPassthrough(ingress.TLSPassthroughOptions{Data: e.data, Conns: e.conns, Log: e.log, BaseDomain: e.cfg.BaseDomain, Meter: e.mx})
	if err := tp.Listen(e.cfg.TLSPassthroughAddr); err != nil {
		return err
	}
	e.cleanup.add(tp.Shutdown)
	e.log.Info("tls passthrough listener", "addr", e.cfg.TLSPassthroughAddr)
	return nil
}

func (e *serverEnv) startSSH(tcp *ingress.TCP) error {
	cfg := e.cfg
	if !cfg.SSHEnabled {
		return nil
	}
	opts := sshfwd.Options{
		Data:               e.data,
		Conns:              e.conns,
		Log:                e.log,
		BaseDomain:         cfg.BaseDomain,
		PublicScheme:       cfg.PublicScheme,
		DisablePathRouting: !cfg.PathRouting,
	}
	if tcp != nil {
		opts.Ports = tcp
	}
	if e.mx != nil {
		opts.Metrics = e.mx
	}
	keyPath := cfg.SSHHostKeyFile
	if keyPath == "" {
		keyPath = defaultHostKeyPath(cfg)
	}
	keyPEM, err := loadOrCreateHostKey(keyPath)
	if err != nil {
		return fmt.Errorf("ssh host key: %w", err)
	}
	opts.HostKeyPEM = keyPEM
	e.log.Info("ssh host key", "path", keyPath)
	srv, err := sshfwd.New(opts)
	if err != nil {
		return err
	}
	if _, err := srv.Listen(cfg.SSHAddr); err != nil {
		return err
	}
	e.cleanup.add(srv.Shutdown)
	e.log.Info("clientless ssh remote-forward listener", "addr", cfg.SSHAddr)
	return nil
}
