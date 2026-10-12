package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dockerStopGraceSeconds = 10

type dockerContainer struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	State string   `json:"State"`
}

func (c dockerContainer) running() bool {
	return c.State == "running"
}

func (c dockerContainer) displayName() string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

type dockerClient struct {
	base string
	http *http.Client

	mu      sync.Mutex
	version string
}

func newDockerClient(host string) (*dockerClient, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse docker host %q: %w", host, err)
	}
	transport := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	var base string
	switch u.Scheme {
	case "unix":
		socketPath := u.Path
		if socketPath == "" {
			return nil, fmt.Errorf("docker host %q has no socket path", host)
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}
		base = "http://docker"
	case "tcp", "http":
		if u.Host == "" {
			return nil, fmt.Errorf("docker host %q has no address", host)
		}
		base = "http://" + u.Host
	default:
		return nil, fmt.Errorf("unsupported docker host scheme %q (want unix://, tcp:// or http://)", u.Scheme)
	}
	return &dockerClient{base: base, http: &http.Client{Transport: transport}}, nil
}

func (c *dockerClient) apiPrefix(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != "" {
		return c.version, nil
	}
	body, status, err := c.do(ctx, http.MethodGet, "/version")
	if err != nil {
		return "", fmt.Errorf("negotiate docker api version: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("negotiate docker api version: %w", apiError(status, body))
	}
	var payload struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("negotiate docker api version: decode: %w", err)
	}
	if payload.APIVersion == "" {
		return "", errors.New("negotiate docker api version: engine reported no ApiVersion")
	}
	c.version = "/v" + payload.APIVersion
	return c.version, nil
}

func (c *dockerClient) do(ctx context.Context, method, pathAndQuery string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+pathAndQuery, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build docker request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("docker %s %s: %w", method, pathAndQuery, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("docker %s %s: read body: %w", method, pathAndQuery, err)
	}
	return body, resp.StatusCode, nil
}

func apiError(status int, body []byte) error {
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Message != "" {
		return fmt.Errorf("docker engine returned %d: %s", status, payload.Message)
	}
	return fmt.Errorf("docker engine returned %d", status)
}

func (c *dockerClient) listContainers(ctx context.Context, filters map[string][]string) ([]dockerContainer, error) {
	prefix, err := c.apiPrefix(ctx)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(filters)
	if err != nil {
		return nil, fmt.Errorf("encode docker filters: %w", err)
	}
	query := url.Values{"all": {"1"}, "filters": {string(encoded)}}
	body, status, err := c.do(ctx, http.MethodGet, prefix+"/containers/json?"+query.Encode())
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list containers: %w", apiError(status, body))
	}
	var containers []dockerContainer
	if err := json.Unmarshal(body, &containers); err != nil {
		return nil, fmt.Errorf("list containers: decode: %w", err)
	}
	return containers, nil
}

func (c *dockerClient) startContainer(ctx context.Context, id string) error {
	return c.containerAction(ctx, id, "start", "")
}

func (c *dockerClient) stopContainer(ctx context.Context, id string) error {
	return c.containerAction(ctx, id, "stop", "?t="+strconv.Itoa(dockerStopGraceSeconds))
}

func (c *dockerClient) containerAction(ctx context.Context, id, action, query string) error {
	prefix, err := c.apiPrefix(ctx)
	if err != nil {
		return err
	}
	body, status, err := c.do(ctx, http.MethodPost, prefix+"/containers/"+url.PathEscape(id)+"/"+action+query)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusNotModified {
		return fmt.Errorf("%s container %s: %w", action, id, apiError(status, body))
	}
	return nil
}
