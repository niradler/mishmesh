package controlplane

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

var updateOpenAPI = flag.Bool("update-openapi", false, "rewrite docs/openapi.json from the registered routes")

var openAPIFile = filepath.Join("..", "..", "docs", "openapi.json")

type openAPIDoc struct {
	Paths map[string]map[string]struct {
		OperationID string              `json:"operationId"`
		Security    []map[string]any    `json:"security"`
		Parameters  []map[string]any    `json:"parameters"`
		RequestBody map[string]any      `json:"requestBody"`
		Responses   map[string]struct{} `json:"responses"`
	} `json:"paths"`
}

func fullOpenAPISpec(t *testing.T) []byte {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetDomainVerification(true, nil)
	api.SetReachInEnabled(true)
	api.Register(http.NewServeMux())
	if len(api.openAPISpec) == 0 {
		t.Fatal("openapi spec was not built")
	}
	return api.openAPISpec
}

func TestOpenAPISpecFileIsCurrent(t *testing.T) {
	want := fullOpenAPISpec(t)
	if *updateOpenAPI {
		if err := os.WriteFile(openAPIFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(openAPIFile)
	if err != nil {
		t.Fatalf("read %s: %v; run make openapi", openAPIFile, err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("docs/openapi.json does not match the registered API routes; run make openapi")
	}
}

func TestOpenAPISpecCoversRoutes(t *testing.T) {
	var doc openAPIDoc
	if err := json.Unmarshal(fullOpenAPISpec(t), &doc); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]string)
	for path, methods := range doc.Paths {
		for method, op := range methods {
			if prev, dup := seen[op.OperationID]; dup {
				t.Errorf("operationId %q used by %s and %s %s", op.OperationID, prev, method, path)
			}
			seen[op.OperationID] = method + " " + path
			if len(op.Responses) == 0 {
				t.Errorf("%s %s has no responses", method, path)
			}
		}
	}

	tests := []struct {
		path, method, operationID string
		secured, body             bool
		params                    []string
	}{
		{"/api/v1/services/{id}/start", "post", "post_services_by_id_start", true, true, []string{"id"}},
		{"/api/v1/services", "get", "get_services", true, false, nil},
		{"/api/v1/reach/{agent_id}/http", "post", "post_reach_by_agent_id_http", true, true, []string{"agent_id"}},
		{"/api/v1/domains/{id}", "delete", "delete_domains_by_id", true, false, []string{"id"}},
		{"/api/v1/auth/login", "post", "post_auth_login", false, true, nil},
		{"/healthz", "get", "get_healthz", false, false, nil},
		{"/api/v1/openapi.json", "get", "get_openapi_json", false, false, nil},
		{"/.well-known/oauth-authorization-server", "get", "get_well_known_oauth_authorization_server", false, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			op, ok := doc.Paths[tc.path][tc.method]
			if !ok {
				t.Fatalf("missing operation")
			}
			if op.OperationID != tc.operationID {
				t.Errorf("operationId = %q, want %q", op.OperationID, tc.operationID)
			}
			if secured := len(op.Security) > 0; secured != tc.secured {
				t.Errorf("secured = %v, want %v", secured, tc.secured)
			}
			if body := op.RequestBody != nil; body != tc.body {
				t.Errorf("request body = %v, want %v", body, tc.body)
			}
			var params []string
			for _, p := range op.Parameters {
				params = append(params, p["name"].(string))
			}
			if len(params) != len(tc.params) || (len(params) > 0 && params[0] != tc.params[0]) {
				t.Errorf("params = %v, want %v", params, tc.params)
			}
		})
	}
}

func TestOpenAPIPathAndParams(t *testing.T) {
	tests := []struct {
		pattern, path string
		params        []string
	}{
		{"/api/v1/services", "/api/v1/services", nil},
		{"/api/v1/orgs/{id}/members/{user_id}", "/api/v1/orgs/{id}/members/{user_id}", []string{"id", "user_id"}},
		{"/files/{rest...}", "/files/{rest}", []string{"rest"}},
		{"/root/{$}", "/root/", nil},
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			path, params := openAPIPathAndParams(tc.pattern)
			if path != tc.path {
				t.Errorf("path = %q, want %q", path, tc.path)
			}
			if len(params) != len(tc.params) {
				t.Fatalf("params = %v, want %v", params, tc.params)
			}
			for i, p := range params {
				if p["name"] != tc.params[i] {
					t.Errorf("param %d = %v, want %s", i, p["name"], tc.params[i])
				}
			}
		})
	}
}

func TestOpenAPIServedWithoutAuth(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "admin-secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + openAPIPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d, content-type = %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var doc openAPIDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Paths["/api/v1/domains"]; ok {
		t.Error("disabled domain routes are listed in the served spec")
	}
	if _, ok := doc.Paths["/api/v1/services/{id}"]["get"]; !ok {
		t.Error("served spec is missing GET /api/v1/services/{id}")
	}
}
