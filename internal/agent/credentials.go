package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const (
	AgentClientID       = "mishmesh-agent"
	credentialsFile     = "credentials.json"
	lockTimeout         = 30 * time.Second
	lockRetry           = 100 * time.Millisecond
	maxTokenBodyBytes   = 64 << 10
	tokenRequestTimeout = 30 * time.Second
)

type Credentials struct {
	Server       string    `json:"server"`
	OrgID        string    `json:"org_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (c *Credentials) needsRefresh(now time.Time) bool {
	half := c.ExpiresAt.Sub(c.IssuedAt) / 2
	return !now.Before(c.IssuedAt.Add(half))
}

type CredentialStore struct {
	path   string
	client *http.Client
	now    func() time.Time
}

func DefaultCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".mishmesh", credentialsFile), nil
}

func NewCredentialStore(path string, client *http.Client) *CredentialStore {
	return &CredentialStore{path: path, client: httpClientOrDefault(client, tokenRequestTimeout), now: time.Now}
}

func (s *CredentialStore) Path() string { return s.path }

func (s *CredentialStore) Load() (*Credentials, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrLoginRequired
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil || c.RefreshToken == "" || c.Server == "" {
		return nil, fmt.Errorf("%w: credentials file %s is unreadable", ErrLoginRequired, s.path)
	}
	return &c, nil
}

func (s *CredentialStore) Save(ctx context.Context, c *Credentials) error {
	return s.locked(ctx, func() error { return s.write(c) })
}

func (s *CredentialStore) Remove(ctx context.Context) error {
	return s.locked(ctx, func() error {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove credentials: %w", err)
		}
		return nil
	})
}

func (s *CredentialStore) AccessToken(ctx context.Context) (string, error) {
	var out string
	err := s.locked(ctx, func() error {
		c, err := s.Load()
		if err != nil {
			return err
		}
		now := s.now()
		if !c.needsRefresh(now) {
			out = c.AccessToken
			return nil
		}
		fresh, err := refreshCredentials(ctx, s.client, c, now)
		if err != nil {
			if !errors.Is(err, ErrLoginRequired) && now.Before(c.ExpiresAt) {
				out = c.AccessToken
				return nil
			}
			return err
		}
		if err := s.write(fresh); err != nil {
			return err
		}
		out = fresh.AccessToken
		return nil
	})
	return out, err
}

func (s *CredentialStore) locked(ctx context.Context, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create credentials directory: %w", err)
	}
	lock := flock.New(s.path + ".lock")
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	ok, err := lock.TryLockContext(lockCtx, lockRetry)
	if err != nil || !ok {
		return fmt.Errorf("lock credentials %s: %w", s.path, errors.Join(err, lockCtx.Err()))
	}
	defer lock.Unlock()
	return fn()
}

func (s *CredentialStore) write(c *Credentials) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, credentialsFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect credentials: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("install credentials: %w", err)
	}
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	OrgID        string `json:"org_id"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
	Interval     int    `json:"interval"`
}

func refreshCredentials(ctx context.Context, client *http.Client, c *Credentials, now time.Time) (*Credentials, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {AgentClientID}, "refresh_token": {c.RefreshToken}}
	resp, status, err := postToken(ctx, client, c.Server, form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		if resp.Error == "invalid_grant" {
			return nil, fmt.Errorf("%w: refresh rejected", ErrLoginRequired)
		}
		return nil, fmt.Errorf("refresh failed: status %d %s", status, resp.Error)
	}
	return credentialsFrom(c.Server, resp, now)
}

func credentialsFrom(server string, resp tokenResponse, now time.Time) (*Credentials, error) {
	if resp.AccessToken == "" || resp.RefreshToken == "" || resp.ExpiresIn <= 0 {
		return nil, errors.New("token response is missing tokens")
	}
	return &Credentials{
		Server:       server,
		OrgID:        resp.OrgID,
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		IssuedAt:     now.UTC(),
		ExpiresAt:    now.UTC().Add(time.Duration(resp.ExpiresIn) * time.Second),
	}, nil
}

func postToken(ctx context.Context, client *http.Client, server string, form url.Values) (tokenResponse, int, error) {
	return postForm(ctx, client, strings.TrimRight(server, "/")+"/oauth2/token", form)
}

func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) (tokenResponse, int, error) {
	var out tokenResponse
	status, err := postFormInto(ctx, client, endpoint, form, "token", &out)
	return out, status, err
}

func postFormInto(ctx context.Context, client *http.Client, endpoint string, form url.Values, what string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("contact %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if err := decodeLimited(resp.Body, out); err != nil && !errors.Is(err, io.EOF) && resp.StatusCode == http.StatusOK {
		return resp.StatusCode, fmt.Errorf("decode %s response: %w", what, err)
	}
	return resp.StatusCode, nil
}

func httpClientOrDefault(client *http.Client, timeout time.Duration) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: timeout}
}

func decodeLimited(r io.Reader, out any) error {
	return json.NewDecoder(io.LimitReader(r, maxTokenBodyBytes)).Decode(out)
}
