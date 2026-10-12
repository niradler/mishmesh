package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

type networkControl struct {
	services  *services.Manager
	prober    *health.Prober
	frontDoor *cloudflare.FrontDoor
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func newNetworkControl(cfg config.Server, data store.DataStore, conns store.ConnectionStore, guard *proxy.Guard, connector *proxy.Connector, log *slog.Logger) (*networkControl, error) {
	nc := &networkControl{}
	if cfg.ServicesFile != "" {
		svcCfg, err := services.LoadFile(cfg.ServicesFile)
		if err != nil {
			return nil, fmt.Errorf("services file: %w", err)
		}
		if nc.services, err = services.NewManager(svcCfg, services.Options{Log: log}); err != nil {
			return nil, fmt.Errorf("services: %w", err)
		}
		log.Info("services enabled", "file", cfg.ServicesFile, "count", len(nc.services.Names()))
	}

	probeOpts := health.Options{Data: data, Conns: conns, Guard: guard, Interval: cfg.HealthInterval, Timeout: cfg.HealthTimeout, Log: log}
	if nc.services != nil {
		probeOpts.Services = nc.services
	}
	nc.prober = health.NewProber(probeOpts)
	connector.SetTargetHealth(nc.prober)

	fdCfg, err := cloudflare.ConfigFromEnv(os.Getenv, cfg.IngressAddr)
	if err != nil {
		return nil, fmt.Errorf("cloudflare front door: %w", err)
	}
	if fdCfg != nil {
		nc.frontDoor = cloudflare.New(fdCfg, cloudflare.StoreSource{Data: data, Orgs: fdCfg.Orgs}, log)
		log.Info("cloudflare front door enabled", "orgs", fdCfg.Orgs)
	}
	return nc, nil
}

func (nc *networkControl) attach(cp *controlplane.API) {
	cp.SetHealth(nc.prober)
	if nc.services != nil {
		cp.SetServices(nc.services)
	}
	if nc.frontDoor != nil {
		cp.SetFrontDoor(nc.frontDoor)
	}
}

func (nc *networkControl) ingressWaker() ingress.ServiceWaker {
	if nc.services == nil {
		return nil
	}
	return nc.services
}

func (nc *networkControl) start() {
	ctx, cancel := context.WithCancel(context.Background())
	nc.cancel = cancel
	nc.spawn(ctx, nc.prober.Run)
	if nc.services != nil {
		nc.spawn(ctx, nc.services.Run)
	}
	if nc.frontDoor != nil {
		nc.spawn(ctx, nc.frontDoor.Run)
	}
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
