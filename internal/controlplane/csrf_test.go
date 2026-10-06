package controlplane

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCSRFRejectsCrossSiteSessionRequests(t *testing.T) {
	srv := newModeAPI(t, "org")
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)
	host := strings.TrimPrefix(srv.URL, "http://")

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		want    int
	}{
		{"json no origin", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/json"}, http.StatusCreated},
		{"json charset", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/json; charset=utf-8"}, http.StatusCreated},
		{"json same origin", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/json", "Origin": "http://" + host, "Sec-Fetch-Site": "same-origin"}, http.StatusCreated},
		{"text plain blind post", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"form encoded blind post", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"missing content type", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{}, http.StatusUnsupportedMediaType},
		{"tenant subdomain origin", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/json", "Origin": "http://evil.localhost:8080"}, http.StatusForbidden},
		{"null origin", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/json", "Origin": "null"}, http.StatusForbidden},
		{"sec fetch cross-site", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"sec fetch same-site without origin", http.MethodPost, "/api/v1/agents", `{"name":"a"}`, map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"bodyless post from other origin", http.MethodPost, "/api/v1/auth/logout", "", map[string]string{"Origin": "http://evil.localhost:8080"}, http.StatusForbidden},
		{"login csrf", http.MethodPost, "/api/v1/auth/login", `{"email":"owner@example.com","password":"supersecret"}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"safe method ignores origin", http.MethodGet, "/api/v1/agents", "", map[string]string{"Origin": "http://evil.localhost:8080"}, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, body)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := owner.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status %d want %d: %s", resp.StatusCode, tc.want, b)
			}
		})
	}
}

func TestCSRFAllowedOriginsAndBearerExempt(t *testing.T) {
	srv, api := newInviteAPI(t, "org")
	api.SetAllowedOrigins([]string{"https://app.example.com"})
	owner, _ := register(t, srv, "owner@example.com", http.StatusCreated)

	post := func(c *http.Client, origin, auth string) int {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/agents", strings.NewReader(`{"name":"a"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := post(owner, "https://app.example.com", ""); got != http.StatusCreated {
		t.Fatalf("configured app origin: status %d", got)
	}
	if got := post(owner, "https://evil.example.com", ""); got != http.StatusForbidden {
		t.Fatalf("foreign origin: status %d", got)
	}
	if got := post(&http.Client{}, "https://evil.example.com", "Bearer admin-secret"); got != http.StatusCreated {
		t.Fatalf("bearer token must be exempt: status %d", got)
	}
}
