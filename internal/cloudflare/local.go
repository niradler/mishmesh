package cloudflare

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	localTimeout       = 3 * time.Second
	localConfigLimit   = 4 << 20
	localMetricsLimit  = 8 << 20
	serverLocationsKey = "cloudflared_tunnel_server_locations{"
	buildInfoKey       = "build_info{"
)

type ConnectorStatus struct {
	Reachable        bool     `json:"reachable"`
	ReadyConnections int      `json:"ready_connections"`
	ConnectorID      string   `json:"connector_id,omitempty"`
	Version          string   `json:"version,omitempty"`
	EdgeLocations    []string `json:"edge_locations"`
	Error            string   `json:"error,omitempty"`
}

type LocalReader struct {
	addr   string
	client *http.Client
}

func NewLocalReader(addr string) (*LocalReader, error) {
	if err := CheckLoopback(addr); err != nil {
		return nil, err
	}
	return &LocalReader{
		addr: addr,
		client: &http.Client{
			Timeout:       localTimeout,
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (l *LocalReader) get(ctx context.Context, path string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+l.addr+path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cloudflared at %s is not reachable: %w", l.addr, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read cloudflared %s: %w", path, err)
	}
	return body, resp.StatusCode, nil
}

func (l *LocalReader) Ingress(ctx context.Context) ([]IngressRule, error) {
	body, code, err := l.get(ctx, "/config", localConfigLimit)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("cloudflared /config returned %d", code)
	}
	var doc struct {
		Config struct {
			Ingress []IngressRule `json:"ingress"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode cloudflared /config: %w", err)
	}
	return doc.Config.Ingress, nil
}

func (l *LocalReader) Status(ctx context.Context) ConnectorStatus {
	st := ConnectorStatus{EdgeLocations: []string{}}
	body, _, err := l.get(ctx, "/ready", 64<<10)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Reachable = true
	var ready struct {
		ReadyConnections int    `json:"readyConnections"`
		ConnectorID      string `json:"connectorId"`
	}
	if err := json.Unmarshal(body, &ready); err != nil {
		st.Error = "cloudflared /ready returned an unexpected body"
		return st
	}
	st.ReadyConnections = ready.ReadyConnections
	st.ConnectorID = ready.ConnectorID
	metrics, code, err := l.get(ctx, "/metrics", localMetricsLimit)
	if err != nil || code != http.StatusOK {
		if err == nil {
			err = fmt.Errorf("cloudflared /metrics returned %d", code)
		}
		st.Error = err.Error()
		return st
	}
	st.EdgeLocations, st.Version = parseMetrics(string(metrics))
	return st
}

func parseMetrics(text string) ([]string, string) {
	locations := map[string]bool{}
	version := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, serverLocationsKey):
			labels, value, ok := splitSample(line)
			if ok && value != "0" {
				if loc := labelValue(labels, "edge_location"); loc != "" {
					locations[loc] = true
				}
			}
		case strings.HasPrefix(line, buildInfoKey):
			if labels, _, ok := splitSample(line); ok {
				version = labelValue(labels, "version")
			}
		}
	}
	out := make([]string, 0, len(locations))
	for loc := range locations {
		out = append(out, loc)
	}
	sort.Strings(out)
	return out, version
}

func splitSample(line string) (string, string, bool) {
	open := strings.IndexByte(line, '{')
	end := strings.LastIndexByte(line, '}')
	if open < 0 || end < open {
		return "", "", false
	}
	return line[open+1 : end], strings.TrimSpace(line[end+1:]), true
}

func labelValue(labels, name string) string {
	for _, part := range strings.Split(labels, ",") {
		key, value, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(key) == name {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return ""
}

type DetectResult struct {
	AccountID string          `json:"account_id,omitempty"`
	TunnelID  string          `json:"tunnel_id,omitempty"`
	Connector ConnectorStatus `json:"connector"`
	Hostnames []string        `json:"hostnames"`
}

func Detect(ctx context.Context, metricsAddr, tunnelToken string) (DetectResult, error) {
	res := DetectResult{Hostnames: []string{}}
	if strings.TrimSpace(tunnelToken) != "" {
		tok, err := DecodeTunnelToken(tunnelToken)
		if err != nil {
			return res, err
		}
		res.AccountID, res.TunnelID = tok.AccountID, tok.TunnelID
	}
	reader, err := NewLocalReader(metricsAddr)
	if err != nil {
		return res, err
	}
	res.Connector = reader.Status(ctx)
	if !res.Connector.Reachable {
		return res, nil
	}
	rules, err := reader.Ingress(ctx)
	if err != nil {
		res.Connector.Error = err.Error()
		return res, nil
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if h := normalizeHost(r.Hostname()); h != "" && !seen[h] {
			seen[h] = true
			res.Hostnames = append(res.Hostnames, h)
		}
	}
	sort.Strings(res.Hostnames)
	return res, nil
}
