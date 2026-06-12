package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestAgentLifecycle(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })

	mux := http.NewServeMux()
	New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var created struct {
		Agent agentDTO `json:"agent"`
		Token string   `json:"token"`
	}
	do(t, srv, http.MethodPost, "/api/v1/agents", `{"name":"web"}`, http.StatusCreated, &created)
	if created.Token == "" || created.Agent.ID == "" {
		t.Fatal("expected agent id and token")
	}
	id := created.Agent.ID

	var list []agentDTO
	do(t, srv, http.MethodGet, "/api/v1/agents?org_id=org_default", "", http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("list: %+v", list)
	}

	var rotated struct {
		Token string `json:"token"`
	}
	do(t, srv, http.MethodPost, "/api/v1/agents/"+id+"/rotate", "", http.StatusCreated, &rotated)
	if rotated.Token == "" || rotated.Token == created.Token {
		t.Fatal("rotate should issue a new token")
	}

	var toks []tokenDTO
	do(t, srv, http.MethodGet, "/api/v1/agents/"+id+"/tokens", "", http.StatusOK, &toks)
	if len(toks) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(toks))
	}

	do(t, srv, http.MethodDelete, "/api/v1/agents/"+id, "", http.StatusConflict, nil)

	do(t, srv, http.MethodPost, "/api/v1/agents/"+id+"/revoke", "", http.StatusOK, nil)

	var got agentDTO
	do(t, srv, http.MethodGet, "/api/v1/agents/"+id, "", http.StatusOK, &got)
	if got.Status != "revoked" {
		t.Fatalf("status: %q", got.Status)
	}

	do(t, srv, http.MethodDelete, "/api/v1/agents/"+id, "", http.StatusNoContent, nil)
	do(t, srv, http.MethodGet, "/api/v1/agents/"+id, "", http.StatusNotFound, nil)
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, wantStatus int, out any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d want %d: %s", method, path, resp.StatusCode, wantStatus, b)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
}
