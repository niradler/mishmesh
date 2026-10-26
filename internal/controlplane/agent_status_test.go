package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

var errNoStream = errors.New("no stream")

type closeSpyConn struct {
	id     string
	closed atomic.Bool
}

func (c *closeSpyConn) AgentID() string { return c.id }
func (c *closeSpyConn) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	return nil, errNoStream
}
func (c *closeSpyConn) Close() error { c.closed.Store(true); return nil }

func newAgentTestServer(t *testing.T) (*httptest.Server, store.DataStore, store.ConnectionStore) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()
	mux := http.NewServeMux()
	New(data, conns, "", slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, data, conns
}

func TestPatchAgentStatusEnforcesLikeRevoke(t *testing.T) {
	cases := []struct {
		status        string
		wantTokenLive bool
		wantAudit     string
	}{
		{store.AgentRevoked, false, "agent.revoke"},
		{store.AgentDisabled, true, "agent.disable"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			srv, data, conns := newAgentTestServer(t)
			var created struct {
				Agent agentDTO `json:"agent"`
			}
			do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"web"}`, http.StatusCreated, &created)
			id := created.Agent.ID
			spy := &closeSpyConn{id: id}
			conns.AddAgent(spy)

			do(t, srv, http.MethodPatch, "/api/v1/agents/"+id, `{"status":"`+tc.status+`"}`, http.StatusOK, nil)

			if !spy.closed.Load() {
				t.Fatal("live agent connection survived the status change")
			}
			toks, err := data.ListTokensByAgent(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			live := false
			for _, tok := range toks {
				if tok.RevokedAt == nil {
					live = true
				}
			}
			if live != tc.wantTokenLive {
				t.Fatalf("token live = %v, want %v", live, tc.wantTokenLive)
			}
			assertAudit(t, data, tc.wantAudit, id)
		})
	}
}

func TestPatchRevokeDeleteAgentAreAudited(t *testing.T) {
	srv, data, _ := newAgentTestServer(t)
	var created struct {
		Agent agentDTO `json:"agent"`
	}
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"web"}`, http.StatusCreated, &created)
	id := created.Agent.ID
	do(t, srv, http.MethodPatch, "/api/v1/agents/"+id, `{"name":"renamed"}`, http.StatusOK, nil)
	assertAudit(t, data, "agent.update", id)
	do(t, srv, http.MethodPost, "/api/v1/agents/"+id+"/revoke", "", http.StatusOK, nil)
	assertAudit(t, data, "agent.revoke", id)
	do(t, srv, http.MethodDelete, "/api/v1/agents/"+id, "", http.StatusNoContent, nil)
	assertAudit(t, data, "agent.delete", id)
}

func assertAudit(t *testing.T, data store.DataStore, action, target string) {
	t.Helper()
	events, err := data.ListAudit(context.Background(), "org_default", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Action == action && ev.Target == target {
			return
		}
	}
	t.Fatalf("no audit entry %s for %s in %d events", action, target, len(events))
}
