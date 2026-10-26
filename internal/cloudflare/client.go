package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout   = 30 * time.Second
	maxRetries       = 3
	defaultBackoff   = 500 * time.Millisecond
	maxRetryAfter    = 10 * time.Second
	maxResponseBytes = 16 << 20
)

type Client struct {
	base    string
	token   string
	http    *http.Client
	retries int
	backoff time.Duration
}

func NewClient(baseURL, apiToken string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		base:    strings.TrimRight(baseURL, "/"),
		token:   apiToken,
		http:    &http.Client{Timeout: requestTimeout},
		retries: maxRetries,
		backoff: defaultBackoff,
	}
}

type APIMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type APIError struct {
	Status   int
	Messages []APIMessage
}

func (e *APIError) Error() string {
	if len(e.Messages) == 0 {
		return fmt.Sprintf("cloudflare api: http %d", e.Status)
	}
	parts := make([]string, 0, len(e.Messages))
	for _, m := range e.Messages {
		parts = append(parts, fmt.Sprintf("%d %s", m.Code, m.Message))
	}
	return fmt.Sprintf("cloudflare api: http %d: %s", e.Status, strings.Join(parts, "; "))
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []APIMessage    `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*envelope, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		payload = b
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, path, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%s %s: read response: %w", method, path, readErr)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < c.retries {
			if err := sleepCtx(ctx, c.retryDelay(resp, attempt)); err != nil {
				return nil, err
			}
			continue
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			if resp.StatusCode >= 300 {
				return nil, &APIError{Status: resp.StatusCode}
			}
			return nil, fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
		if resp.StatusCode >= 300 || !env.Success {
			return nil, &APIError{Status: resp.StatusCode, Messages: env.Errors}
		}
		return &env, nil
	}
}

func (c *Client) retryDelay(resp *http.Response, attempt int) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return min(time.Duration(secs)*time.Second, maxRetryAfter)
		}
	}
	return c.backoff << attempt
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type Connection struct {
	ID                 string `json:"id"`
	ColoName           string `json:"colo_name"`
	IsPendingReconnect bool   `json:"is_pending_reconnect"`
	OriginIP           string `json:"origin_ip"`
	OpenedAt           string `json:"opened_at"`
	ClientVersion      string `json:"client_version"`
}

type Tunnel struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Status      string       `json:"status"`
	Connections []Connection `json:"connections"`
}

func (c *Client) GetTunnel(ctx context.Context, accountID, tunnelID string) (*Tunnel, error) {
	env, err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/cfd_tunnel/"+tunnelID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get tunnel: %w", err)
	}
	var t Tunnel
	if err := json.Unmarshal(env.Result, &t); err != nil {
		return nil, fmt.Errorf("decode tunnel: %w", err)
	}
	if t.Connections == nil {
		t.Connections = []Connection{}
	}
	return &t, nil
}

type IngressRule struct {
	fields map[string]json.RawMessage
}

func NewIngressRule(hostname, service string) IngressRule {
	fields := map[string]json.RawMessage{}
	if hostname != "" {
		fields["hostname"], _ = json.Marshal(hostname)
	}
	fields["service"], _ = json.Marshal(service)
	return IngressRule{fields: fields}
}

func (r IngressRule) stringField(name string) string {
	raw, ok := r.fields[name]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func (r IngressRule) Hostname() string { return r.stringField("hostname") }
func (r IngressRule) Service() string  { return r.stringField("service") }
func (r IngressRule) Path() string     { return r.stringField("path") }

func (r *IngressRule) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &r.fields)
}

func (r IngressRule) MarshalJSON() ([]byte, error) {
	if r.fields == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(r.fields)
}

type TunnelConfig struct {
	Ingress []IngressRule
	fields  map[string]json.RawMessage
}

func (c *TunnelConfig) UnmarshalJSON(b []byte) error {
	c.fields = map[string]json.RawMessage{}
	c.Ingress = nil
	if err := json.Unmarshal(b, &c.fields); err != nil {
		return err
	}
	if raw, ok := c.fields["ingress"]; ok {
		if err := json.Unmarshal(raw, &c.Ingress); err != nil {
			return fmt.Errorf("decode ingress: %w", err)
		}
		delete(c.fields, "ingress")
	}
	return nil
}

func (c TunnelConfig) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(c.fields)+1)
	for k, v := range c.fields {
		out[k] = v
	}
	rules := c.Ingress
	if rules == nil {
		rules = []IngressRule{}
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, err
	}
	out["ingress"] = raw
	return json.Marshal(out)
}

func (c *Client) GetTunnelConfig(ctx context.Context, accountID, tunnelID string) (*TunnelConfig, error) {
	env, err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/cfd_tunnel/"+tunnelID+"/configurations", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get tunnel configuration: %w", err)
	}
	var result struct {
		Config *TunnelConfig `json:"config"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return nil, fmt.Errorf("decode tunnel configuration: %w", err)
	}
	if result.Config == nil {
		return &TunnelConfig{}, nil
	}
	return result.Config, nil
}

func (c *Client) PutTunnelConfig(ctx context.Context, accountID, tunnelID string, cfg *TunnelConfig) error {
	_, err := c.do(ctx, http.MethodPut, "/accounts/"+accountID+"/cfd_tunnel/"+tunnelID+"/configurations", nil, map[string]any{"config": cfg})
	if err != nil {
		return fmt.Errorf("put tunnel configuration: %w", err)
	}
	return nil
}

type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) ListZones(ctx context.Context, accountID string) ([]Zone, error) {
	var zones []Zone
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("per_page", "50")
		q.Set("page", strconv.Itoa(page))
		q.Set("account.id", accountID)
		env, err := c.do(ctx, http.MethodGet, "/zones", q, nil)
		if err != nil {
			return nil, fmt.Errorf("list zones: %w", err)
		}
		var batch []Zone
		if err := json.Unmarshal(env.Result, &batch); err != nil {
			return nil, fmt.Errorf("decode zones: %w", err)
		}
		zones = append(zones, batch...)
		if page >= env.ResultInfo.TotalPages || len(batch) == 0 {
			return zones, nil
		}
	}
}

type DNSRecord struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Content  string         `json:"content"`
	Proxied  bool           `json:"proxied"`
	Comment  string         `json:"comment,omitempty"`
	TTL      int            `json:"ttl,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
}

func (c *Client) ListDNSRecords(ctx context.Context, zoneID, name string) ([]DNSRecord, error) {
	q := url.Values{}
	q.Set("name", name)
	env, err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records", q, nil)
	if err != nil {
		return nil, fmt.Errorf("list dns records: %w", err)
	}
	var recs []DNSRecord
	if err := json.Unmarshal(env.Result, &recs); err != nil {
		return nil, fmt.Errorf("decode dns records: %w", err)
	}
	return recs, nil
}

func (c *Client) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) error {
	_, err := c.createDNSRecord(ctx, zoneID, rec)
	return err
}

func (c *Client) createDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (DNSRecord, error) {
	envelope, err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", nil, rec)
	if err != nil {
		return DNSRecord{}, fmt.Errorf("create dns record: %w", err)
	}
	var created DNSRecord
	if err := json.Unmarshal(envelope.Result, &created); err != nil {
		return DNSRecord{}, errors.New("invalid created DNS record")
	}
	return created, nil
}

func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	if _, err := c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil, nil); err != nil {
		return fmt.Errorf("delete dns record: %w", err)
	}
	return nil
}

type AccessApp map[string]any

func (c *Client) GetAccessApp(ctx context.Context, accountID, appID string) (AccessApp, error) {
	env, err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/access/apps/"+appID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get access app: %w", err)
	}
	var app AccessApp
	if err := decodeAccessObject(env.Result, &app); err != nil {
		return nil, fmt.Errorf("decode access app: %w", err)
	}
	return app, nil
}

func (c *Client) PutAccessApp(ctx context.Context, accountID, appID string, app AccessApp) error {
	if _, err := c.do(ctx, http.MethodPut, "/accounts/"+accountID+"/access/apps/"+appID, nil, app); err != nil {
		return fmt.Errorf("put access app: %w", err)
	}
	return nil
}

type AccessPolicy map[string]any

func decodeAccessObject(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}

func (c *Client) CreateAccessApp(ctx context.Context, accountID string, app AccessApp) (AccessApp, error) {
	env, err := c.do(ctx, http.MethodPost, "/accounts/"+accountID+"/access/apps", nil, app)
	if err != nil {
		return nil, fmt.Errorf("create access app: %w", err)
	}
	var created AccessApp
	if err := decodeAccessObject(env.Result, &created); err != nil {
		return nil, fmt.Errorf("decode access app: %w", err)
	}
	return created, nil
}

func (c *Client) DeleteAccessApp(ctx context.Context, accountID, appID string) error {
	if _, err := c.do(ctx, http.MethodDelete, "/accounts/"+accountID+"/access/apps/"+appID, nil, nil); err != nil {
		return fmt.Errorf("delete access app: %w", err)
	}
	return nil
}

func (c *Client) GetAccessPolicy(ctx context.Context, accountID, policyID string) (AccessPolicy, error) {
	env, err := c.do(ctx, http.MethodGet, "/accounts/"+accountID+"/access/policies/"+policyID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get access policy: %w", err)
	}
	var policy AccessPolicy
	if err := decodeAccessObject(env.Result, &policy); err != nil {
		return nil, fmt.Errorf("decode access policy: %w", err)
	}
	return policy, nil
}

func (c *Client) CreateAccessPolicy(ctx context.Context, accountID string, policy AccessPolicy) (AccessPolicy, error) {
	env, err := c.do(ctx, http.MethodPost, "/accounts/"+accountID+"/access/policies", nil, policy)
	if err != nil {
		return nil, fmt.Errorf("create access policy: %w", err)
	}
	var created AccessPolicy
	if err := decodeAccessObject(env.Result, &created); err != nil {
		return nil, fmt.Errorf("decode access policy: %w", err)
	}
	return created, nil
}

func (c *Client) PutAccessPolicy(ctx context.Context, accountID, policyID string, policy AccessPolicy) error {
	if _, err := c.do(ctx, http.MethodPut, "/accounts/"+accountID+"/access/policies/"+policyID, nil, policy); err != nil {
		return fmt.Errorf("put access policy: %w", err)
	}
	return nil
}

func (c *Client) DeleteAccessPolicy(ctx context.Context, accountID, policyID string) error {
	if _, err := c.do(ctx, http.MethodDelete, "/accounts/"+accountID+"/access/policies/"+policyID, nil, nil); err != nil {
		return fmt.Errorf("delete access policy: %w", err)
	}
	return nil
}

func isNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
