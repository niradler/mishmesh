package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestPasswordAuthSessionFlow(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "id.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("localhost:8080", "http")
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	doc(t, client, srv, http.MethodGet, "/api/v1/agents", "", http.StatusUnauthorized, nil)

	var reg struct {
		Org string `json:"org"`
	}
	doc(t, client, srv, http.MethodPost, "/api/v1/auth/register", `{"email":"owner@example.com","password":"supersecret","name":"Owner"}`, http.StatusCreated, &reg)
	if reg.Org == "" {
		t.Fatal("expected org assignment on register")
	}

	var me struct {
		Role string `json:"role"`
	}
	doc(t, client, srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusOK, &me)
	if me.Role != "owner" {
		t.Fatalf("first user should be owner, got %q", me.Role)
	}

	doc(t, client, srv, http.MethodPost, "/api/v1/agents", `{"name":"web"}`, http.StatusCreated, nil)

	doc(t, client, srv, http.MethodPost, "/api/v1/auth/logout", "", http.StatusNoContent, nil)
	doc(t, client, srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusUnauthorized, nil)
}

func TestAuthConfigEndpoint(t *testing.T) {
	data, _ := sqlite.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var cfg map[string]bool
	doc(t, &http.Client{}, srv, http.MethodGet, "/api/v1/auth/config", "", http.StatusOK, &cfg)
	if !cfg["auth_enabled"] || !cfg["password_enabled"] || cfg["google_enabled"] {
		t.Fatalf("auth config: %+v", cfg)
	}
}

func doc(t *testing.T, client *http.Client, srv *httptest.Server, method, path, body string, wantStatus int, out any) {
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
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d want %d: %s", method, path, resp.StatusCode, wantStatus, b)
	}
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
}
