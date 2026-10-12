package cloudflare

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfigFromEnv(t *testing.T) {
	base := map[string]string{
		"MISHMESH_CLOUDFLARE_API_TOKEN":  "tok",
		"MISHMESH_CLOUDFLARE_ACCOUNT_ID": "acct",
		"MISHMESH_CLOUDFLARE_TUNNEL_ID":  "tun",
	}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	tests := []struct {
		name    string
		env     map[string]string
		ingress string
		wantNil bool
		wantErr bool
		check   func(t *testing.T, c *Config)
	}{
		{name: "disabled without token", env: map[string]string{}, ingress: "127.0.0.1:8080", wantNil: true},
		{name: "missing account", env: map[string]string{"MISHMESH_CLOUDFLARE_API_TOKEN": "tok", "MISHMESH_CLOUDFLARE_TUNNEL_ID": "t"}, ingress: "127.0.0.1:8080", wantErr: true},
		{name: "missing tunnel", env: map[string]string{"MISHMESH_CLOUDFLARE_API_TOKEN": "tok", "MISHMESH_CLOUDFLARE_ACCOUNT_ID": "a"}, ingress: "127.0.0.1:8080", wantErr: true},
		{
			name: "defaults", env: base, ingress: "127.0.0.1:8080",
			check: func(t *testing.T, c *Config) {
				if c.Origin != "http://127.0.0.1:8080" || c.Prune || c.SyncInterval != 5*time.Minute ||
					!reflect.DeepEqual(c.Orgs, []string{"org_default"}) || c.BaseURL != DefaultBaseURL || c.Cloudflared != nil || c.AccessAppID != "" {
					t.Fatalf("unexpected defaults: %+v", c)
				}
			},
		},
		{
			name: "explicit values", ingress: "127.0.0.1:8080",
			env: with(map[string]string{
				"MISHMESH_CLOUDFLARE_ORIGIN": "https://ingress.internal", "MISHMESH_CLOUDFLARE_ACCESS_APP_ID": "app",
				"MISHMESH_CLOUDFLARE_PRUNE": "true", "MISHMESH_CLOUDFLARE_SYNC_INTERVAL": "30s", "MISHMESH_CLOUDFLARE_ORGS": "org_a, org_b,",
			}),
			check: func(t *testing.T, c *Config) {
				if c.Origin != "https://ingress.internal" || c.AccessAppID != "app" || !c.Prune || c.SyncInterval != 30*time.Second ||
					!reflect.DeepEqual(c.Orgs, []string{"org_a", "org_b"}) {
					t.Fatalf("unexpected config: %+v", c)
				}
			},
		},
		{name: "bad prune", env: with(map[string]string{"MISHMESH_CLOUDFLARE_PRUNE": "maybe"}), ingress: "127.0.0.1:8080", wantErr: true},
		{name: "bad interval", env: with(map[string]string{"MISHMESH_CLOUDFLARE_SYNC_INTERVAL": "soon"}), ingress: "127.0.0.1:8080", wantErr: true},
		{name: "non-positive interval", env: with(map[string]string{"MISHMESH_CLOUDFLARE_SYNC_INTERVAL": "0s"}), ingress: "127.0.0.1:8080", wantErr: true},
		{name: "no origin and no ingress", env: base, ingress: "", wantErr: true},
		{name: "explicit origin without ingress", env: with(map[string]string{"MISHMESH_CLOUDFLARE_ORIGIN": "http://x:1"}), ingress: "", check: func(t *testing.T, c *Config) {
			if c.Origin != "http://x:1" {
				t.Fatalf("origin = %s", c.Origin)
			}
		}},
		{
			name: "supervised", ingress: "127.0.0.1:8080",
			env: with(map[string]string{"MISHMESH_CLOUDFLARED_RUN": "true", "MISHMESH_CLOUDFLARED_TOKEN_FILE": "/run/secrets/cf"}),
			check: func(t *testing.T, c *Config) {
				want := &SupervisorConfig{Bin: "cloudflared", TokenFile: "/run/secrets/cf", MetricsAddr: "127.0.0.1:20241"}
				if !reflect.DeepEqual(c.Cloudflared, want) {
					t.Fatalf("supervisor = %+v", c.Cloudflared)
				}
			},
		},
		{
			name: "supervised custom", ingress: "127.0.0.1:8080",
			env: with(map[string]string{"MISHMESH_CLOUDFLARED_RUN": "1", "MISHMESH_CLOUDFLARED_BIN": "/opt/cf", "MISHMESH_CLOUDFLARED_TOKEN_FILE": "tok", "MISHMESH_CLOUDFLARED_METRICS_ADDR": "127.0.0.1:9999"}),
			check: func(t *testing.T, c *Config) {
				want := &SupervisorConfig{Bin: "/opt/cf", TokenFile: "tok", MetricsAddr: "127.0.0.1:9999"}
				if !reflect.DeepEqual(c.Cloudflared, want) {
					t.Fatalf("supervisor = %+v", c.Cloudflared)
				}
			},
		},
		{name: "supervised without token file", env: with(map[string]string{"MISHMESH_CLOUDFLARED_RUN": "true"}), ingress: "127.0.0.1:8080", wantErr: true},
		{name: "supervised false ignores token file", env: with(map[string]string{"MISHMESH_CLOUDFLARED_RUN": "false"}), ingress: "127.0.0.1:8080", check: func(t *testing.T, c *Config) {
			if c.Cloudflared != nil {
				t.Fatalf("supervisor configured")
			}
		}},
		{name: "bad run flag", env: with(map[string]string{"MISHMESH_CLOUDFLARED_RUN": "x"}), ingress: "127.0.0.1:8080", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ConfigFromEnv(envFrom(tt.env), tt.ingress)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if cfg != nil {
					t.Fatalf("expected nil config, got %+v", cfg)
				}
				return
			}
			tt.check(t, cfg)
		})
	}
}

type stubStore struct {
	store.DataStore
	byOrg map[string][]*store.Endpoint
	err   error
}

func (s stubStore) ListEndpointsByOrg(_ context.Context, org string) ([]*store.Endpoint, error) {
	return s.byOrg[org], s.err
}

func TestStoreSource(t *testing.T) {
	cf := &store.EndpointPolicy{FrontDoor: store.FrontDoorCloudflare}
	data := stubStore{byOrg: map[string][]*store.Endpoint{
		"org_default": {
			{ID: "e1", OrgID: "org_default", Domain: "B.Example.com.", Policy: cf},
			{ID: "e2", OrgID: "org_default", Domain: "", Policy: cf},
			{ID: "e3", OrgID: "org_default", Domain: "c.example.com", Policy: &store.EndpointPolicy{}},
			{ID: "e4", OrgID: "org_default", Domain: "d.example.com"},
			{ID: "e5", OrgID: "org_default", Domain: "a.example.com", Policy: cf},
		},
		"org_other": {{ID: "e6", OrgID: "org_other", Domain: "z.example.com", Policy: cf}},
		"org_skip":  {{ID: "e7", OrgID: "org_skip", Domain: "skip.example.com", Policy: cf}},
	}}
	src := StoreSource{Data: data, Orgs: []string{"org_default", "org_other"}}
	got, err := src.DesiredHostnames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Hostname{
		{Host: "a.example.com", EndpointID: "e5", OrgID: "org_default"},
		{Host: "b.example.com", EndpointID: "e1", OrgID: "org_default"},
		{Host: "z.example.com", EndpointID: "e6", OrgID: "org_other"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStoreSourceError(t *testing.T) {
	src := StoreSource{Data: stubStore{err: context.DeadlineExceeded}, Orgs: []string{"org_default"}}
	if _, err := src.DesiredHostnames(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
