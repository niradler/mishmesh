package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPAHandlerDoesNotServeSiblingDirectory(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	sibling := filepath.Join(root, "web-secret")
	for _, dir := range []string{web, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(web, "index.html"), "INDEX")
	write(filepath.Join(web, "app.js"), "APP")
	write(filepath.Join(sibling, "token.txt"), "SECRET")
	handler := spaHandler(web)

	tests := []struct {
		name       string
		path       string
		wantBody   string
		wantStatus int
	}{
		{"asset", "/app.js", "APP", http.StatusOK},
		{"unknown route falls back to index", "/dashboard/agents", "INDEX", http.StatusOK},
		{"dotdot to sibling", "/../web-secret/token.txt", "404 page not found\n", http.StatusNotFound},
		{"encoded dotdot to sibling", "/%2e%2e/web-secret/token.txt", "INDEX", http.StatusOK},
		{"deep dotdot to sibling", "/a/../../web-secret/token.txt", "invalid URL path\n", http.StatusBadRequest},
		{"backslash dotdot to sibling", `/..\web-secret\token.txt`, "404 page not found\n", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.URL.Path = tt.path
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			body := rec.Body.String()
			if strings.Contains(body, "SECRET") {
				t.Fatalf("sibling directory content leaked: %q", body)
			}
			if rec.Code != tt.wantStatus || body != tt.wantBody {
				t.Fatalf("status=%d body=%q, want %d %q", rec.Code, body, tt.wantStatus, tt.wantBody)
			}
		})
	}
}
