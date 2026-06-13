package controlplane

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newTestAPI(t *testing.T) (*API, *httptest.Server) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("localhost:8080", "http")
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return api, srv
}

func TestQuotaEnforcement(t *testing.T) {
	api, srv := newTestAPI(t)
	api.SetDefaultQuota(store.Quota{MaxAgents: 1, MaxEndpoints: 1})

	var created struct {
		Agent agentDTO `json:"agent"`
	}
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, &created)
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"b"}`, http.StatusConflict, nil)

	var q quotaDTO
	do(t, srv, http.MethodGet, "/api/v1/quota", "", http.StatusOK, &q)
	if q.MaxAgents != 1 || q.Usage.Agents != 1 {
		t.Fatalf("quota dto: %+v", q)
	}

	do(t, srv, http.MethodPut, "/api/v1/quota", `{"max_agents":5,"max_endpoints":10,"max_bandwidth_bytes":0}`, http.StatusOK, nil)
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"c"}`, http.StatusCreated, nil)
}

func TestReservedEndpointWithPolicy(t *testing.T) {
	_, srv := newTestAPI(t)
	var created struct {
		Agent agentDTO `json:"agent"`
	}
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, &created)

	body := `{"agent_id":"` + created.Agent.ID + `","kind":"http","subdomain":"shop","policy":{"basic_auth_user":"alice","basic_auth_password":"s3cret","force_https":true}}`
	var ep endpointDTO
	do(t, srv, http.MethodPost, "/api/v1/endpoints", body, http.StatusCreated, &ep)
	if ep.PublicURL != "http://shop.localhost:8080" {
		t.Fatalf("public url: %q", ep.PublicURL)
	}
	if ep.Policy == nil || ep.Policy.BasicAuthHash != "set" {
		t.Fatalf("policy should redact basic auth hash: %+v", ep.Policy)
	}

	var list []endpointDTO
	do(t, srv, http.MethodGet, "/api/v1/endpoints", "", http.StatusOK, &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(list))
	}

	do(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"policy":{"compression":true}}`, http.StatusOK, nil)
	do(t, srv, http.MethodDelete, "/api/v1/endpoints/"+ep.ID, "", http.StatusNoContent, nil)
}

func TestStatusAndAudit(t *testing.T) {
	_, srv := newTestAPI(t)
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, nil)

	var status map[string]any
	do(t, srv, http.MethodGet, "/api/v1/status", "", http.StatusOK, &status)
	if status["agents"] == nil || status["endpoints"] == nil {
		t.Fatalf("status missing fields: %+v", status)
	}

	var audit []auditDTO
	do(t, srv, http.MethodGet, "/api/v1/audit", "", http.StatusOK, &audit)
	found := false
	for _, e := range audit {
		if e.Action == "agent.create" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected agent.create audit event, got %+v", audit)
	}
}
