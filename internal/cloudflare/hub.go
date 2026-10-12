package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

var (
	ErrNotConnected = errors.New("no cloudflare connection for this organization")
	errHubStopped   = errors.New("cloudflare hub is not running")
)

type HubOptions struct {
	Data           store.DataStore
	Box            *secrets.Box
	Log            *slog.Logger
	Origin         string
	CloudflaredBin string
	BaseURL        string
	SyncInterval   time.Duration
}

type Hub struct {
	opts HubOptions
	src  Source
	log  *slog.Logger

	reloadMu sync.Mutex
	mu       sync.RWMutex
	ctx      context.Context
	orgs     map[string]*orgRun
	loadErrs map[string]string
}

type orgRun struct {
	pub  *Publisher
	stop context.CancelFunc
	done chan struct{}
	sup  *supervisorRun
}

type supervisorRun struct {
	cfg  SupervisorConfig
	sup  *Supervisor
	stop context.CancelFunc
	done chan struct{}
}

func NewHub(opts HubOptions) *Hub {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.CloudflaredBin == "" {
		opts.CloudflaredBin = DefaultCloudflared
	}
	return &Hub{
		opts:     opts,
		src:      StoreSource{Data: opts.Data},
		log:      opts.Log,
		orgs:     map[string]*orgRun{},
		loadErrs: map[string]string{},
	}
}

func (h *Hub) DefaultOrigin() string {
	return h.opts.Origin
}

func OriginForListenAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func (h *Hub) Start(ctx context.Context) error {
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
	conns, err := h.opts.Data.ListCloudflareConnections(ctx)
	if err != nil {
		return fmt.Errorf("list cloudflare connections: %w", err)
	}
	for _, conn := range conns {
		if err := h.Reload(ctx, conn.OrgID); err != nil {
			h.log.Error("cloudflare connection not loaded", "org", conn.OrgID, "error", err)
		}
	}
	return nil
}

func (h *Hub) Close() {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.Lock()
	runs := h.orgs
	h.orgs = map[string]*orgRun{}
	h.ctx = nil
	h.mu.Unlock()
	for _, run := range runs {
		stopRun(run)
		if run.sup != nil {
			run.sup.halt()
		}
	}
}

func (h *Hub) Reload(ctx context.Context, orgID string) error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	h.mu.RLock()
	base := h.ctx
	h.mu.RUnlock()
	if base == nil {
		return errHubStopped
	}
	conn, err := h.opts.Data.GetCloudflareConnection(ctx, orgID)
	if errors.Is(err, store.ErrNotFound) {
		h.remove(orgID, "")
		return nil
	}
	if err != nil {
		return fmt.Errorf("load cloudflare connection: %w", err)
	}
	cfg, err := h.ConfigFor(conn)
	if err != nil {
		h.remove(orgID, err.Error())
		return err
	}
	pub, err := NewPublisher(cfg, h.src, h.log)
	if err != nil {
		h.remove(orgID, err.Error())
		return err
	}

	h.mu.Lock()
	old := h.orgs[orgID]
	h.mu.Unlock()
	if old != nil {
		stopRun(old)
	}
	var sup *supervisorRun
	if cfg.Connector == ConnectorManaged {
		want := SupervisorConfig{Bin: cfg.CloudflaredBin, Token: cfg.TunnelToken, MetricsAddr: cfg.MetricsAddr}
		if old != nil && old.sup != nil && old.sup.cfg == want {
			sup = old.sup
		} else {
			if old != nil && old.sup != nil {
				old.sup.halt()
			}
			sup = startSupervisor(base, want, h.log.With("org", orgID))
		}
	} else if old != nil && old.sup != nil {
		old.sup.halt()
	}
	if sup != nil {
		pub.supervisor = sup.sup
	}
	runCtx, stop := context.WithCancel(base)
	run := &orgRun{pub: pub, stop: stop, done: make(chan struct{}), sup: sup}
	go func() {
		defer close(run.done)
		pub.Run(runCtx)
	}()
	h.mu.Lock()
	h.orgs[orgID] = run
	delete(h.loadErrs, orgID)
	h.mu.Unlock()
	return nil
}

func (h *Hub) remove(orgID, loadErr string) {
	h.mu.Lock()
	old := h.orgs[orgID]
	delete(h.orgs, orgID)
	if loadErr == "" {
		delete(h.loadErrs, orgID)
	} else {
		h.loadErrs[orgID] = loadErr
	}
	h.mu.Unlock()
	if old != nil {
		stopRun(old)
		if old.sup != nil {
			old.sup.halt()
		}
	}
}

func stopRun(run *orgRun) {
	run.stop()
	<-run.done
}

func startSupervisor(ctx context.Context, cfg SupervisorConfig, log *slog.Logger) *supervisorRun {
	supCtx, stop := context.WithCancel(ctx)
	run := &supervisorRun{cfg: cfg, sup: NewSupervisor(cfg, log), stop: stop, done: make(chan struct{})}
	go func() {
		defer close(run.done)
		run.sup.Run(supCtx)
	}()
	return run
}

func (s *supervisorRun) halt() {
	s.stop()
	<-s.done
}

func (h *Hub) ConfigFor(conn *store.CloudflareConnection) (*Config, error) {
	if h.opts.Box == nil {
		return nil, errors.New("secret key is not configured")
	}
	tunnelToken, err := h.open(conn.TunnelTokenEnc, conn.OrgID, TokenKindTunnel)
	if err != nil {
		return nil, err
	}
	if tunnelToken == "" {
		return nil, errors.New("stored tunnel token is empty")
	}
	apiToken, err := h.open(conn.APITokenEnc, conn.OrgID, TokenKindAPI)
	if err != nil {
		return nil, err
	}
	origin := conn.Origin
	if origin == "" {
		origin = h.opts.Origin
	}
	if origin == "" {
		return nil, errors.New("origin is required when the server has no ingress address")
	}
	return &Config{
		OrgID:          conn.OrgID,
		Name:           conn.Name,
		AccountID:      conn.AccountID,
		TunnelID:       conn.TunnelID,
		TunnelToken:    tunnelToken,
		APIToken:       apiToken,
		AccessAppID:    conn.AccessAppID,
		Origin:         origin,
		Connector:      conn.Connector,
		MetricsAddr:    conn.MetricsAddr,
		Prune:          conn.Prune,
		SyncInterval:   h.opts.SyncInterval,
		BaseURL:        h.opts.BaseURL,
		CloudflaredBin: h.opts.CloudflaredBin,
	}, nil
}

func (h *Hub) open(sealed []byte, orgID, kind string) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	plain, err := h.opts.Box.Open(sealed, TokenContext(orgID, kind))
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", kind, err)
	}
	return string(plain), nil
}

func (h *Hub) publisher(orgID string) *Publisher {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if run := h.orgs[orgID]; run != nil {
		return run.pub
	}
	return nil
}

func (h *Hub) Status(orgID string) Status {
	if pub := h.publisher(orgID); pub != nil {
		return pub.Status()
	}
	h.mu.RLock()
	loadErr := h.loadErrs[orgID]
	h.mu.RUnlock()
	return Status{Level: LevelNone, LastError: loadErr, Connector: ConnectorStatus{EdgeLocations: []string{}}, Hostnames: []HostStatus{}}
}

func (h *Hub) Sync(ctx context.Context, orgID string) (Status, error) {
	pub := h.publisher(orgID)
	if pub == nil {
		return h.Status(orgID), ErrNotConnected
	}
	return pub.Sync(ctx)
}

func (h *Hub) Trigger(orgID string) {
	if pub := h.publisher(orgID); pub != nil {
		pub.Trigger()
	}
}
