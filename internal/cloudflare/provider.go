package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type ProviderOptions struct {
	CloudflaredBin string
	BaseURL        string
	NewClient      func(string, string) *Client
	Detect         func(context.Context, string, string) (DetectResult, error)
	NewSupervisor  func(SupervisorConfig, *slog.Logger) *Supervisor
}

type provider struct {
	options ProviderOptions
	locks   providerLocks
}

type providerConfigFields struct {
	AccountID   string `json:"account_id"`
	TunnelID    string `json:"tunnel_id"`
	AccessAppID string `json:"access_app_id"`
	Origin      string `json:"origin"`
	Connector   string `json:"connector"`
	MetricsAddr string `json:"metrics_addr"`
	Prune       bool   `json:"prune"`
}

var providerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func NewProvider(options ProviderOptions) connections.Provider {
	if options.NewClient == nil {
		options.NewClient = NewClient
	}
	if options.Detect == nil {
		options.Detect = Detect
	}
	if options.NewSupervisor == nil {
		options.NewSupervisor = NewSupervisor
	}
	return &provider{options: options}
}

func (p *provider) Describe() connections.Descriptor {
	return connections.Descriptor{Type: "cloudflare", Label: "Cloudflare Tunnel", Role: "access", Scope: "public", Protocols: []string{store.KindHTTP}, Levels: []string{LevelImported, LevelManaged}, Detect: true, DetectHostCommand: true,
		DetectFields: []typedfields.Field{{Key: "metrics_addr", Label: "Metrics address", Kind: "text"}, {Key: TokenKindTunnel, Label: "Tunnel token", Kind: "secret"}},
		Help:         "Observe an existing tunnel or manage its ingress, DNS and Access hostnames.", Fields: []typedfields.Field{
			{Key: "account_id", Label: "Account ID", Kind: "text"},
			{Key: "tunnel_id", Label: "Tunnel ID", Kind: "text"},
			{Key: "access_app_id", Label: "Access application ID", Kind: "text"},
			{Key: "origin", Label: "Origin", Kind: "text", Help: "Defaults to the server ingress address."},
			{Key: "connector", Label: "Connector", Kind: "select", Options: []string{ConnectorExternal, ConnectorManaged}, Default: json.RawMessage(`"external"`)},
			{Key: "metrics_addr", Label: "Metrics address", Kind: "text", Default: json.RawMessage(`"127.0.0.1:20241"`)},
			{Key: "prune", Label: "Prune", Kind: "bool"},
			{Key: TokenKindTunnel, Label: "Tunnel token", Kind: "secret", Required: true},
			{Key: TokenKindAPI, Label: "API token", Kind: "secret"},
		}}
}

func (p *provider) configuration(input connections.Config) (*Config, error) {
	if input.Record.ID == "" || input.Record.OrgID == "" || input.Record.Type != "cloudflare" {
		return nil, errors.New("invalid cloudflare connection identity")
	}
	fields := make([]typedfields.Field, 0)
	for _, field := range p.Describe().Fields {
		if field.Kind != "secret" {
			fields = append(fields, field)
		}
	}
	if err := typedfields.Validate(fields, input.Record.Config); err != nil {
		return nil, errors.New("invalid cloudflare configuration")
	}
	var values providerConfigFields
	if err := json.Unmarshal(input.Record.Config, &values); err != nil {
		return nil, errors.New("invalid cloudflare configuration")
	}
	for key, value := range input.Secrets {
		if key != TokenKindTunnel && key != TokenKindAPI || len(value) > 16384 || strings.ContainsRune(value, 0) {
			return nil, errors.New("invalid cloudflare secrets")
		}
	}
	tunnelToken := strings.TrimSpace(input.Secrets[TokenKindTunnel])
	token, err := DecodeTunnelToken(tunnelToken)
	if err != nil {
		return nil, err
	}
	if !providerIDPattern.MatchString(token.AccountID) || !providerIDPattern.MatchString(token.TunnelID) || values.AccountID != "" && values.AccountID != token.AccountID || values.TunnelID != "" && values.TunnelID != token.TunnelID {
		return nil, errors.New("account and tunnel IDs must match the tunnel token")
	}
	if values.AccessAppID != "" && !providerIDPattern.MatchString(values.AccessAppID) {
		return nil, errors.New("invalid Access application ID")
	}
	if values.MetricsAddr == "" {
		values.MetricsAddr = DefaultMetricsAddr
	}
	if err := CheckLoopback(values.MetricsAddr); err != nil {
		return nil, errors.New("metrics address must be a loopback host:port")
	}
	if values.Connector == "" {
		values.Connector = ConnectorExternal
	}
	if values.Origin != "" {
		if err := checkProviderOrigin(values.Origin); err != nil {
			return nil, err
		}
	}
	apiToken := strings.TrimSpace(input.Secrets[TokenKindAPI])
	if values.Prune && apiToken == "" {
		return nil, errors.New("prune needs an API token (managed level)")
	}
	return &Config{ConnectionID: input.Record.ID, OrgID: input.Record.OrgID, Name: input.Record.Name, AccountID: token.AccountID, TunnelID: token.TunnelID, AccessAppID: values.AccessAppID,
		Origin: values.Origin, Connector: values.Connector, MetricsAddr: values.MetricsAddr, Prune: values.Prune, TunnelToken: tunnelToken, APIToken: apiToken,
		BaseURL: p.options.BaseURL, CloudflaredBin: p.options.CloudflaredBin}, nil
}

func checkProviderOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("origin must be an HTTP or HTTPS URL without credentials, path, query or fragment")
	}
	return nil
}

func (p *provider) Validate(input connections.Config) error {
	_, err := p.configuration(input)
	return err
}

func (p *provider) UsesHostCommands(input connections.Config) bool {
	var values providerConfigFields
	return json.Unmarshal(input.Record.Config, &values) != nil || values.Connector == ConnectorManaged
}

func (p *provider) AccessHostname(endpointDomain, requested string) (string, error) {
	return accessHostname(endpointDomain, requested)
}

func accessHostname(endpointDomain, requested string) (string, error) {
	if requested == "" {
		requested = endpointDomain
	}
	host, err := connections.NormalizeAccessHostname(requested)
	if err != nil || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return "", errors.New("Cloudflare access hostname must be a full DNS domain")
	}
	return host, nil
}

func (p *provider) Detect(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	fields := []typedfields.Field{{Key: "metrics_addr", Kind: "text"}, {Key: TokenKindTunnel, Kind: "secret"}}
	if err := typedfields.Validate(fields, input); err != nil {
		return nil, errors.New("invalid cloudflare detection input")
	}
	var values struct {
		MetricsAddr string `json:"metrics_addr"`
		TunnelToken string `json:"tunnel_token"`
	}
	if err := json.Unmarshal(input, &values); err != nil {
		return nil, errors.New("invalid cloudflare detection input")
	}
	if values.MetricsAddr == "" {
		values.MetricsAddr = DefaultMetricsAddr
	}
	if err := CheckLoopback(values.MetricsAddr); err != nil {
		return nil, errors.New("metrics address must be a loopback host:port")
	}
	if values.TunnelToken != "" {
		if len(values.TunnelToken) > 16384 {
			return nil, errors.New("invalid tunnel token")
		}
		if _, err := DecodeTunnelToken(values.TunnelToken); err != nil {
			return nil, err
		}
	}
	result, err := p.options.Detect(ctx, values.MetricsAddr, values.TunnelToken)
	if err != nil {
		return nil, errors.New("cloudflare detection failed")
	}
	if result.Connector.Error != "" {
		result.Connector.Error = "cloudflared observation failed"
	}
	return json.Marshal(result)
}

func (p *provider) Open(input connections.Config, deps connections.Deps) (connections.Instance, error) {
	cfg, err := p.configuration(input)
	if err != nil {
		return nil, err
	}
	if deps.Data == nil {
		return nil, errors.New("endpoint source is required")
	}
	if cfg.Origin == "" {
		cfg.Origin = deps.Origin
	}
	if err := checkProviderOrigin(cfg.Origin); err != nil {
		return nil, err
	}
	pub, err := NewPublisher(cfg, StoreSource{Data: deps.Data, ConnectionID: cfg.ConnectionID}, deps.Log)
	if err != nil {
		return nil, errors.New("cloudflare publisher could not be opened")
	}
	pub.publishAliases = func(ctx context.Context, status *Status) error {
		observed := providerStatus(*status)
		if err := connections.RecordAccessAliases(ctx, input.Record, p.Describe(), &observed, deps); err != nil {
			for index := range status.Hostnames {
				if status.Hostnames[index].State == StatePublished {
					status.Hostnames[index].State = StateError
					status.Hostnames[index].Detail = "Access routing changed or could not be saved; retry sync"
				}
			}
			return err
		}
		return nil
	}
	if cfg.Level() == LevelManaged {
		pub.client = p.options.NewClient(cfg.BaseURL, cfg.APIToken)
		if pub.client == nil {
			return nil, errors.New("cloudflare client is required")
		}
		pub.ownership, err = openOwnership(input.Record.State, cfg, deps.Data)
		if err != nil {
			return nil, err
		}
		pub.locks = &p.locks
	}
	if cfg.Connector == ConnectorManaged {
		pub.supervisor = p.options.NewSupervisor(SupervisorConfig{Bin: cfg.CloudflaredBin, Token: cfg.TunnelToken, MetricsAddr: cfg.MetricsAddr}, deps.Log)
		if pub.supervisor == nil {
			return nil, errors.New("cloudflared supervisor is required")
		}
	}
	return &providerInstance{publisher: pub}, nil
}

type providerInstance struct {
	publisher *Publisher
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	closed    bool
}

func (i *providerInstance) Run(ctx context.Context) {
	i.mu.Lock()
	if i.closed || i.done != nil {
		i.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	i.cancel, i.done = cancel, make(chan struct{})
	done := i.done
	i.mu.Unlock()
	defer close(done)
	defer cancel()
	var supervisors sync.WaitGroup
	if i.publisher.supervisor != nil {
		supervisors.Add(1)
		go func() { defer supervisors.Done(); i.publisher.supervisor.Run(runCtx) }()
	}
	i.publisher.Run(runCtx)
	cancel()
	supervisors.Wait()
}

func (i *providerInstance) Close() {
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

func (i *providerInstance) Sync(ctx context.Context) (connections.Status, error) {
	status, err := i.publisher.Sync(ctx)
	return providerStatus(status), err
}

func (i *providerInstance) Status() connections.Status { return providerStatus(i.publisher.Status()) }

func providerStatus(status Status) connections.Status {
	result := connections.Status{Level: status.Level, Health: "unknown", LastError: status.LastError, LastSync: status.LastSync, Hostnames: make([]connections.HostStatus, 0, len(status.Hostnames)), Details: make(map[string]any)}
	if status.Tunnel != nil {
		result.Health = status.Tunnel.Status
		if result.Health != "healthy" && result.Health != "down" {
			result.Health = "unknown"
		}
		result.Details["tunnel"] = *status.Tunnel
	}
	connector := status.Connector
	if connector.Error != "" {
		connector.Error = "cloudflared observation failed"
	}
	result.Details["connector"], result.Details["origin"] = connector, status.Origin
	if status.Supervisor != nil {
		supervisor := *status.Supervisor
		if supervisor.LastExit != "" {
			supervisor.LastExit = "cloudflared exited"
		}
		result.Details["supervisor"] = supervisor
	}
	for _, host := range status.Hostnames {
		detail := host.Detail
		if host.State == StateConflict && host.DNS != DNSConflict {
			detail = "hostname is routed to another service in the tunnel configuration"
		} else if host.State == StateUnclaimed && strings.HasPrefix(detail, "routed to ") {
			detail = "hostname is routed directly to another service"
		}
		result.Hostnames = append(result.Hostnames, connections.HostStatus{Host: host.Host, State: host.State, EndpointID: host.EndpointID, Detail: detail, Fix: host.Fix, AccessHostname: host.AccessHostname, AccessHostnameKnown: host.AccessHostnameKnown, EndpointKind: store.KindHTTP})
		if result.Health == "healthy" && (host.State == StateError || host.State == StateConflict || host.State == StatePending) {
			result.Health = "degraded"
		}
	}
	if result.LastError != "" && result.Health == "healthy" {
		result.Health = "degraded"
	}
	return result
}

var _ connections.Instance = (*providerInstance)(nil)
