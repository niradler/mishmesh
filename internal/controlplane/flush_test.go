package controlplane

import (
	"net/http"
	"sync/atomic"
	"testing"
)

func TestMutationsThatChangeDecisionsFlushThem(t *testing.T) {
	api, srv := newTestAPI(t)
	var flushes atomic.Int32
	var unscoped atomic.Bool
	api.SetDecisionFlusher(func(org string) {
		flushes.Add(1)
		if org == "" {
			unscoped.Store(true)
		}
	})

	var created struct {
		Agent agentDTO `json:"agent"`
	}
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"a"}`, http.StatusCreated, &created)
	var ep struct {
		ID string `json:"id"`
	}
	do(t, srv, http.MethodPost, "/api/v1/endpoints", `{"agent_id":"`+created.Agent.ID+`","kind":"http","subdomain":"shop"}`, http.StatusCreated, &ep)

	steps := []struct {
		name, method, path, body string
		status                   int
		flushes                  bool
	}{
		{"endpoint patch", http.MethodPatch, "/api/v1/endpoints/" + ep.ID, `{"subdomain":"shop2"}`, http.StatusOK, true},
		{"agent rename only", http.MethodPatch, "/api/v1/agents/" + created.Agent.ID, `{"name":"b"}`, http.StatusOK, false},
		{"agent disabled", http.MethodPatch, "/api/v1/agents/" + created.Agent.ID, `{"status":"disabled"}`, http.StatusOK, true},
		{"endpoint delete", http.MethodDelete, "/api/v1/endpoints/" + ep.ID, "", http.StatusNoContent, true},
	}
	for _, s := range steps {
		before := flushes.Load()
		do(t, srv, s.method, s.path, s.body, s.status, nil)
		if got := flushes.Load() > before; got != s.flushes {
			t.Errorf("%s: flushed = %v, want %v", s.name, got, s.flushes)
		}
	}
	if unscoped.Load() {
		t.Error("a single-org mutation flushed every org")
	}
}
