package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/connections"
	tailscaleconnection "github.com/mishmesh/mishmesh/internal/connections/tailscale"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

type networkControl struct {
	log                *slog.Logger
	services           *services.Manager
	serviceRegistry    *services.Registry
	invalidateIngress  func()
	prober             *health.Prober
	connectionHub      *connections.Hub
	connectionRegistry *connections.Registry
	box                *secrets.Box
	hostCommands       bool
	cp                 *controlplane.API
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
}

func newNetworkControlWithBox(cfg config.Server, data store.DataStore, conns store.ConnectionStore, guard *proxy.Guard, connector *proxy.Connector, log *slog.Logger, box *secrets.Box) (*networkControl, error) {
	if box == nil {
		return nil, fmt.Errorf("network control requires the data migration secret box")
	}
	nc := &networkControl{log: log, hostCommands: cfg.HostCommands, box: box}
	keyFile := cfg.EffectiveSecretKeyFile()
	driverRegistry := services.NewRegistry()
	for _, driver := range []services.DriverType{services.DockerType{}, services.ComposeType{}, services.CommandType{}} {
		if err := driverRegistry.Register(driver); err != nil {
			return nil, fmt.Errorf("register service driver: %w", err)
		}
	}
	nc.serviceRegistry = driverRegistry
	nc.services = services.NewManager(services.Options{
		Registry:     driverRegistry,
		Log:          log,
		OnChange:     nc.serviceChanged,
		DockerHost:   cfg.DockerHost,
		HostCommands: cfg.HostCommands,
	})
	nc.prober = health.NewProber(health.Options{
		Data: data, Conns: conns, Guard: guard, Services: nc.services,
		Interval: cfg.HealthInterval, Timeout: cfg.HealthTimeout, Log: log,
	})
	connector.SetTargetHealth(nc.prober)
	connectionRegistry := connections.NewRegistry()
	if err := connectionRegistry.Register(cloudflare.NewProvider(cloudflare.ProviderOptions{CloudflaredBin: cfg.CloudflaredBin})); err != nil {
		return nil, fmt.Errorf("register cloudflare connection provider: %w", err)
	}
	if err := connectionRegistry.Register(tailscaleconnection.NewProvider(tailscaleconnection.Options{})); err != nil {
		return nil, fmt.Errorf("register tailscale connection provider: %w", err)
	}
	nc.connectionRegistry = connectionRegistry
	nc.connectionHub = connections.NewHub(connections.HubOptions{
		Registry: connectionRegistry,
		Deps:     connections.Deps{Data: data, Box: nc.box, Log: log, Origin: cloudflare.OriginForListenAddr(cfg.IngressAddr), HostCommands: cfg.HostCommands, OnAliasesChanged: nc.aliasesChanged},
	})
	log.Info("network control ready", "secret_key_file", keyFile, "host_commands", cfg.HostCommands)
	return nc, nil
}

func (nc *networkControl) serviceChanged() {
	nc.prober.Trigger()
}

func (nc *networkControl) aliasesChanged() {
	if nc.invalidateIngress != nil {
		nc.invalidateIngress()
	}
}

func (nc *networkControl) attach(cp *controlplane.API) {
	nc.cp = cp
	cp.SetHealth(nc.prober)
	cp.SetServices(nc.services)
	cp.SetServiceRegistry(nc.serviceRegistry)
	cp.SetConnections(nc.connectionHub, nc.connectionRegistry, nc.box)
	cp.SetHostCommands(nc.hostCommands)
}

func (nc *networkControl) ingressWaker() ingress.ServiceWaker {
	return nc.services
}

func (nc *networkControl) start() error {
	ctx, cancel := context.WithCancel(context.Background())
	nc.cancel = cancel
	if nc.cp != nil {
		if err := nc.cp.LoadServices(ctx); err != nil {
			cancel()
			return fmt.Errorf("load services: %w", err)
		}
	}
	if err := nc.connectionHub.Start(ctx); err != nil {
		cancel()
		return fmt.Errorf("connections: %w", err)
	}
	nc.spawn(ctx, func(ctx context.Context) {
		nc.services.Refresh(ctx)
		nc.prober.Run(ctx)
	})
	nc.spawn(ctx, nc.services.Run)
	return nil
}

func (nc *networkControl) spawn(ctx context.Context, run func(context.Context)) {
	nc.wg.Add(1)
	go func() {
		defer nc.wg.Done()
		run(ctx)
	}()
}

func (nc *networkControl) stop(ctx context.Context, log *slog.Logger) {
	if nc.cancel == nil {
		return
	}
	nc.cancel()
	nc.connectionHub.Close()
	done := make(chan struct{})
	go func() {
		nc.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		log.Warn("network control did not stop before the drain deadline")
	}
}
