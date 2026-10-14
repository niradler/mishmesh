package tailscaleconnection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type Options struct {
	NewLocalAPI func(string) LocalAPI
	HTTPClient  *http.Client
	APIBaseURL  string
	Interval    time.Duration
}

type provider struct {
	options Options
	locks   targetLocks
}
type configuration struct {
	Level       string   `json:"level"`
	Socket      string   `json:"socket"`
	Tailnet     string   `json:"tailnet"`
	ServiceTags []string `json:"service_tags"`
}

var tagPattern = regexp.MustCompile(`^tag:[a-zA-Z][a-zA-Z0-9-]{0,62}$`)

func NewProvider(options Options) connections.Provider {
	if options.NewLocalAPI == nil {
		options.NewLocalAPI = func(socket string) LocalAPI { return NewLocalAPI(socket, nil) }
	}
	if options.Interval <= 0 {
		options.Interval = 30 * time.Second
	}
	return &provider{options: options}
}

func (p *provider) Describe() connections.Descriptor {
	return connections.Descriptor{Type: "tailscale", Label: "Tailscale Services", Role: "access", Scope: "private", Levels: []string{"imported", "managed"}, Detect: true,
		Help: "Observe or publish HTTPS services on an existing tailnet. Tagged node, host approval and access grants are managed in Tailscale.", Fields: []typedfields.Field{
			{Key: "level", Label: "Management", Kind: "select", Options: []string{"imported", "managed"}, Default: json.RawMessage(`"imported"`)},
			{Key: "socket", Label: "LocalAPI socket", Kind: "text"},
			{Key: "tailnet", Label: "Tailnet", Kind: "text", Required: true},
			{Key: "service_tags", Label: "Service tags", Kind: "list"},
			{Key: "oauth_client_id", Label: "OAuth client ID", Kind: "secret"},
			{Key: "oauth_client_secret", Label: "OAuth client secret", Kind: "secret"},
		}}
}

func (p *provider) configuration(input connections.Config) (configuration, error) {
	var cfg configuration
	if input.Record.ID == "" || input.Record.OrgID == "" || input.Record.Type != "tailscale" {
		return cfg, errors.New("invalid Tailscale connection identity")
	}
	var fields []typedfields.Field
	for _, field := range p.Describe().Fields {
		if field.Kind != "secret" {
			fields = append(fields, field)
		}
	}
	if typedfields.Validate(fields, input.Record.Config) != nil || json.Unmarshal(input.Record.Config, &cfg) != nil {
		return cfg, errors.New("invalid Tailscale configuration")
	}
	if cfg.Level == "" {
		cfg.Level = "imported"
	}
	if len(cfg.Tailnet) > 255 || cfg.Tailnet != strings.TrimSpace(cfg.Tailnet) || strings.ContainsAny(cfg.Tailnet, "/\\?#\r\n\x00") {
		return cfg, errors.New("invalid Tailscale tailnet")
	}
	var err error
	if cfg.Socket, err = canonicalSocket(cfg.Socket); err != nil {
		return cfg, err
	}
	if len(cfg.ServiceTags) > 32 {
		return cfg, errors.New("too many service tags")
	}
	for _, tag := range cfg.ServiceTags {
		if !tagPattern.MatchString(tag) {
			return cfg, errors.New("invalid Tailscale service tag")
		}
	}
	for key, value := range input.Secrets {
		if key != "oauth_client_id" && key != "oauth_client_secret" || len(value) > 16384 || strings.ContainsAny(value, "\r\n\x00") {
			return cfg, errors.New("invalid Tailscale secrets")
		}
	}
	if cfg.Level == "managed" && (input.Secrets["oauth_client_id"] == "" || input.Secrets["oauth_client_secret"] == "") {
		return cfg, errors.New("managed Tailscale requires OAuth credentials")
	}
	return cfg, nil
}

func (p *provider) Validate(input connections.Config) error {
	_, err := p.configuration(input)
	return err
}
func (p *provider) AccessHostname(endpointDomain, requested string) (string, error) {
	return AccessHostname(endpointDomain, requested)
}

func (p *provider) Detect(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if typedfields.Validate([]typedfields.Field{{Key: "socket", Kind: "text"}}, raw) != nil {
		return nil, errors.New("invalid Tailscale detection input")
	}
	var input struct {
		Socket string `json:"socket"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return nil, errors.New("invalid Tailscale detection input")
	}
	socket, err := canonicalSocket(input.Socket)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	local := p.options.NewLocalAPI(socket)
	if local == nil {
		return nil, errors.New("Tailscale LocalAPI unavailable")
	}
	status, err := local.StatusWithoutPeers(ctx)
	if err != nil || status == nil || status.CurrentTailnet == nil {
		return nil, errors.New("Tailscale LocalAPI unavailable")
	}
	return json.Marshal(map[string]any{"socket": socket, "tailnet": status.CurrentTailnet.Name, "version": status.Version, "tagged": status.Self != nil && status.Self.IsTagged()})
}

func (p *provider) Open(input connections.Config, deps connections.Deps) (connections.Instance, error) {
	cfg, err := p.configuration(input)
	if err != nil {
		return nil, err
	}
	if deps.Data == nil {
		return nil, errors.New("Tailscale endpoint source is required")
	}
	origin, err := url.Parse(deps.Origin)
	if err != nil || origin.Scheme != "http" && origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" {
		return nil, errors.New("Tailscale ingress must be an HTTP origin")
	}
	local := p.options.NewLocalAPI(cfg.Socket)
	if local == nil {
		return nil, errors.New("Tailscale LocalAPI is required")
	}
	i := &instance{provider: p, cfg: cfg, record: input.Record, deps: deps, local: local, api: newAPI(p.options.APIBaseURL, p.options.HTTPClient, input.Secrets["oauth_client_id"], input.Secrets["oauth_client_secret"]), origin: deps.Origin}
	if _, err := i.loadState(input.Record.State, ""); err != nil {
		return nil, err
	}
	return i, nil
}

type instance struct {
	provider *provider
	cfg      configuration
	record   store.Connection
	deps     connections.Deps
	local    LocalAPI
	api      *API
	origin   string
	syncMu   sync.Mutex
	mu       sync.Mutex
	status   connections.Status
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

func (i *instance) Run(ctx context.Context) {
	i.mu.Lock()
	if i.closed || i.done != nil {
		i.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	i.cancel, i.done = cancel, make(chan struct{})
	done := i.done
	i.mu.Unlock()
	defer close(done)
	defer cancel()
	ticker := time.NewTicker(i.provider.options.Interval)
	defer ticker.Stop()
	for {
		_, _ = i.Sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (i *instance) Close() {
	i.mu.Lock()
	i.closed = true
	if i.cancel != nil {
		i.cancel()
	}
	done := i.done
	i.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (i *instance) Status() connections.Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	result := i.status
	result.Hostnames = append([]connections.HostStatus(nil), result.Hostnames...)
	return result
}

func (i *instance) Sync(ctx context.Context) (connections.Status, error) {
	i.syncMu.Lock()
	defer i.syncMu.Unlock()
	i.mu.Lock()
	closed := i.closed
	i.mu.Unlock()
	status := connections.Status{Level: i.cfg.Level, Health: "degraded", LastSync: time.Now(), Hostnames: []connections.HostStatus{}}
	var err error
	if closed {
		err = errors.New("Tailscale connection is closed")
	} else {
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		err = i.sync(ctx, &status)
	}
	if err != nil {
		status.LastError = err.Error()
		status.Health = "degraded"
	}
	i.mu.Lock()
	i.status = status
	i.mu.Unlock()
	return i.Status(), err
}
