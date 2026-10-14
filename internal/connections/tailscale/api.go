package tailscaleconnection

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
	"sync"
	"time"
)

type Service struct {
	Name        string                     `json:"name"`
	DisplayName string                     `json:"displayName,omitempty"`
	Addrs       []string                   `json:"addrs,omitempty"`
	Comment     string                     `json:"comment,omitempty"`
	Ports       []string                   `json:"ports,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

func (s *Service) UnmarshalJSON(raw []byte) error {
	type fields Service
	var known fields
	if err := json.Unmarshal(raw, &known); err != nil {
		return err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	for _, key := range []string{"name", "displayName", "addrs", "comment", "ports", "tags"} {
		delete(values, key)
	}
	*s = Service(known)
	if len(values) > 0 {
		s.Extra = values
	}
	return nil
}

func (s Service) MarshalJSON() ([]byte, error) {
	type fields Service
	raw, err := json.Marshal(fields(s))
	if err != nil || len(s.Extra) == 0 {
		return raw, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	for key, value := range s.Extra {
		if _, exists := values[key]; !exists {
			values[key] = value
		}
	}
	return json.Marshal(values)
}

type ServiceHost struct {
	StableNodeID  string `json:"stableNodeID"`
	ApprovalLevel string `json:"approvalLevel"`
	Configured    string `json:"configured"`
}

func (a *API) CheckServiceHosts(ctx context.Context, tailnet, name, stableID string) error {
	if stableID == "" {
		return errors.New("Tailscale local node identity is unavailable; Service cleanup refused")
	}
	var response struct {
		Hosts []ServiceHost `json:"hosts"`
	}
	if err := a.request(ctx, http.MethodGet, servicePath(tailnet, name)+"/devices", nil, &response); err != nil {
		return err
	}
	if response.Hosts == nil {
		return errors.New("Tailscale Service host membership is unknown; cleanup refused")
	}
	for _, host := range response.Hosts {
		if host.StableNodeID == "" || host.Configured == "" || host.ApprovalLevel != "not-approved" && host.ApprovalLevel != "approved:auto" && host.ApprovalLevel != "approved:manual" {
			return errors.New("Tailscale Service host membership is unknown; cleanup refused")
		}
		if host.StableNodeID != stableID {
			return errors.New("Tailscale Service has another host; resolve shared ownership in Tailscale before cleanup")
		}
	}
	return nil
}

type API struct {
	base       string
	client     *http.Client
	id, secret string
	mu         sync.Mutex
	token      string
	expires    time.Time
}

type apiError struct{ status int }

func (e apiError) Error() string {
	return fmt.Sprintf("Tailscale API request failed (HTTP %d)", e.status)
}

func newAPI(base string, client *http.Client, id, secret string) *API {
	if base == "" {
		base = "https://api.tailscale.com"
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.Timeout = 15 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &API{base: strings.TrimSuffix(base, "/"), client: &copyClient, id: id, secret: secret}
}

func (a *API) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.expires) {
		return a.token, nil
	}
	body := url.Values{"grant_type": {"client_credentials"}, "client_id": {a.id}, "client_secret": {a.secret}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/api/v2/oauth/token", strings.NewReader(body))
	if err != nil {
		return "", errors.New("Tailscale OAuth request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.client.Do(req)
	if err != nil {
		return "", errors.New("Tailscale OAuth request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("Tailscale OAuth authentication failed")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if decodeBounded(res.Body, &token) != nil || token.AccessToken == "" || len(token.AccessToken) > 16384 || strings.ContainsAny(token.AccessToken, "\r\n") || !strings.EqualFold(token.TokenType, "bearer") || token.ExpiresIn <= 0 || token.ExpiresIn > 86400 {
		return "", errors.New("invalid Tailscale OAuth response")
	}
	a.token, a.expires = token.AccessToken, time.Now().Add(time.Duration(token.ExpiresIn)*time.Second*9/10)
	return a.token, nil
}

func decodeBounded(body io.Reader, result any) error {
	raw, err := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("Tailscale response too large")
	}
	return json.Unmarshal(raw, result)
}

func (a *API) request(ctx context.Context, method, path string, input, output any) error {
	token, err := a.accessToken(ctx)
	if err != nil {
		return err
	}
	var body []byte
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return errors.New("invalid Tailscale API request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+"/api/v2/"+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("Tailscale API request failed")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return errors.New("Tailscale API request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiError{res.StatusCode}
	}
	if output != nil && decodeBounded(res.Body, output) != nil {
		return errors.New("invalid Tailscale API response")
	}
	return nil
}

func servicePath(tailnet, name string) string {
	return "tailnet/" + url.PathEscape(tailnet) + "/services/" + url.PathEscape(name)
}
func (a *API) GetService(ctx context.Context, tailnet, name string) (*Service, error) {
	var service Service
	err := a.request(ctx, http.MethodGet, servicePath(tailnet, name), nil, &service)
	var remote apiError
	if errors.As(err, &remote) && remote.status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if service.Name != name {
		return nil, errors.New("invalid Tailscale service response")
	}
	return &service, nil
}
func (a *API) PutService(ctx context.Context, tailnet string, service Service) error {
	return a.request(ctx, http.MethodPut, servicePath(tailnet, service.Name), service, nil)
}
func (a *API) DeleteService(ctx context.Context, tailnet, name string) error {
	return a.request(ctx, http.MethodDelete, servicePath(tailnet, name), nil, nil)
}
func (a *API) ValidateNode(ctx context.Context, tailnet, stableID string, numericID int64) error {
	var response struct {
		Devices []struct {
			ID     string `json:"id"`
			NodeID string `json:"nodeId"`
		} `json:"devices"`
	}
	if err := a.request(ctx, http.MethodGet, "tailnet/"+url.PathEscape(tailnet)+"/devices?fields=default", nil, &response); err != nil {
		return err
	}
	for _, device := range response.Devices {
		if device.NodeID == stableID || device.ID == stableID || device.ID == strconv.FormatInt(numericID, 10) {
			return nil
		}
	}
	return errors.New("Tailscale API tailnet does not contain this node")
}
