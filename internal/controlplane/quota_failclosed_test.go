package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

var errInjected = errors.New("injected store failure")

type flakyQuotaStore struct {
	store.DataStore
	failGetQuota      atomic.Bool
	failCountAgents   atomic.Bool
	failCountEndpoint atomic.Bool
}

func (f *flakyQuotaStore) GetQuota(ctx context.Context, orgID string) (*store.Quota, error) {
	if f.failGetQuota.Load() {
		return nil, errInjected
	}
	return f.DataStore.GetQuota(ctx, orgID)
}

func (f *flakyQuotaStore) CountAgents(ctx context.Context, orgID string) (int, error) {
	if f.failCountAgents.Load() {
		return 0, errInjected
	}
	return f.DataStore.CountAgents(ctx, orgID)
}

func (f *flakyQuotaStore) CountEndpoints(ctx context.Context, orgID string) (int, error) {
	if f.failCountEndpoint.Load() {
		return 0, errInjected
	}
	return f.DataStore.CountEndpoints(ctx, orgID)
}

func TestQuotaChecksFailClosedOnStoreErrors(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*flakyQuotaStore)
		method string
		path   string
		body   string
	}{
		{"agent create, quota unreadable", func(f *flakyQuotaStore) { f.failGetQuota.Store(true) }, http.MethodPost, "/api/v1/agents", `{"name":"x"}`},
		{"agent create, count unreadable", func(f *flakyQuotaStore) { f.failCountAgents.Store(true) }, http.MethodPost, "/api/v1/agents", `{"name":"x"}`},
		{"endpoint create, quota unreadable", func(f *flakyQuotaStore) { f.failGetQuota.Store(true) }, http.MethodPost, "/api/v1/endpoints", `{"agent_id":"AGENT","kind":"http","subdomain":"shop"}`},
		{"endpoint create, count unreadable", func(f *flakyQuotaStore) { f.failCountEndpoint.Store(true) }, http.MethodPost, "/api/v1/endpoints", `{"agent_id":"AGENT","kind":"http","subdomain":"shop"}`},
		{"quota read, quota unreadable", func(f *flakyQuotaStore) { f.failGetQuota.Store(true) }, http.MethodGet, "/api/v1/quota", ""},
		{"quota read, count unreadable", func(f *flakyQuotaStore) { f.failCountAgents.Store(true) }, http.MethodGet, "/api/v1/quota", ""},
		{"status, quota unreadable", func(f *flakyQuotaStore) { f.failGetQuota.Store(true) }, http.MethodGet, "/api/v1/status", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sq, err := sqlite.Open(filepath.Join(t.TempDir(), "cp.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sq.Close() })
			data := &flakyQuotaStore{DataStore: sq}
			api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
			api.SetPublicConfig("localhost:8080", "http")
			api.SetDefaultQuota(store.Quota{MaxAgents: 1, MaxEndpoints: 1})
			mux := http.NewServeMux()
			api.Register(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			var created struct {
				Agent agentDTO `json:"agent"`
			}
			do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, &created)
			body := tc.body
			if body != "" {
				body = replaceAgent(body, created.Agent.ID)
			}

			tc.inject(data)
			do(t, srv, tc.method, tc.path, body, http.StatusInternalServerError, nil)
		})
	}
}

func replaceAgent(body, id string) string {
	const marker = "AGENT"
	for i := 0; i+len(marker) <= len(body); i++ {
		if body[i:i+len(marker)] == marker {
			return body[:i] + id + body[i+len(marker):]
		}
	}
	return body
}

func TestQuotaMissingMeansUnlimited(t *testing.T) {
	_, srv := newTestAPI(t)
	for i := 0; i < 3; i++ {
		do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, nil)
	}
}
