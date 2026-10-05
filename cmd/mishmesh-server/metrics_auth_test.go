package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetricsAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	tests := []struct {
		name         string
		metricsToken string
		apiToken     string
		header       string
		want         int
	}{
		{"open when nothing configured", "", "", "", http.StatusOK},
		{"api token required without header", "", "api", "", http.StatusUnauthorized},
		{"api token accepted as fallback", "", "api", "Bearer api", http.StatusOK},
		{"wrong token rejected", "", "api", "Bearer nope", http.StatusUnauthorized},
		{"dedicated token required", "scrape", "api", "Bearer scrape", http.StatusOK},
		{"api token rejected when dedicated set", "scrape", "api", "Bearer api", http.StatusUnauthorized},
		{"no header with dedicated", "scrape", "", "", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := metricsAuth(tc.metricsToken, tc.apiToken, ok)
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d", rec.Code, tc.want)
			}
		})
	}
}
