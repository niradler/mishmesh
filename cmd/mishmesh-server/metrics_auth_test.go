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
		remoteAddr   string
		extraHeaders map[string]string
		want         int
	}{
		{"loopback v4 open when nothing configured", "", "", "", "127.0.0.1:5555", nil, http.StatusOK},
		{"loopback v6 open when nothing configured", "", "", "", "[::1]:5555", nil, http.StatusOK},
		{"non-loopback rejected when nothing configured", "", "", "", "192.0.2.1:1234", nil, http.StatusUnauthorized},
		{"private non-loopback rejected", "", "", "", "10.0.0.5:1234", nil, http.StatusUnauthorized},
		{"unparseable remote rejected", "", "", "", "garbage", nil, http.StatusUnauthorized},
		{"x-forwarded-for rejected", "", "", "", "127.0.0.1:5555", map[string]string{"X-Forwarded-For": "203.0.113.9"}, http.StatusUnauthorized},
		{"forwarded rejected", "", "", "", "127.0.0.1:5555", map[string]string{"Forwarded": "for=203.0.113.9"}, http.StatusUnauthorized},
		{"x-real-ip rejected", "", "", "", "127.0.0.1:5555", map[string]string{"X-Real-IP": "203.0.113.9"}, http.StatusUnauthorized},
		{"cf-connecting-ip rejected", "", "", "", "127.0.0.1:5555", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, http.StatusUnauthorized},
		{"api token required without header", "", "api", "", "127.0.0.1:5555", nil, http.StatusUnauthorized},
		{"api token accepted as fallback", "", "api", "Bearer api", "192.0.2.1:1234", nil, http.StatusOK},
		{"api token accepted through proxy headers", "", "api", "Bearer api", "127.0.0.1:5555", map[string]string{"X-Forwarded-For": "203.0.113.9"}, http.StatusOK},
		{"wrong token rejected", "", "api", "Bearer nope", "127.0.0.1:5555", nil, http.StatusUnauthorized},
		{"dedicated token required", "scrape", "api", "Bearer scrape", "127.0.0.1:5555", nil, http.StatusOK},
		{"api token rejected when dedicated set", "scrape", "api", "Bearer api", "127.0.0.1:5555", nil, http.StatusUnauthorized},
		{"no header with dedicated", "scrape", "", "", "127.0.0.1:5555", nil, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := metricsAuth(tc.metricsToken, tc.apiToken, ok)
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			for k, v := range tc.extraHeaders {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d", rec.Code, tc.want)
			}
		})
	}
}
