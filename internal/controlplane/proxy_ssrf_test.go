package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestProxyEndpointTargetValidation(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		allowed bool
	}{
		{"metadata", "169.254.169.254:80", false},
		{"loopback", "127.0.0.1:5432", false},
		{"rfc1918", "10.0.0.5:80", false},
		{"cgnat", "100.64.1.1:80", false},
		{"ula", "[fd00::1]:80", false},
		{"cluster local", "kubernetes.default.svc.cluster.local:443", false},
		{"svc", "db.prod.svc:5432", false},
		{"single label", "redis:6379", false},
		{"missing port", "example.com", false},
		{"public", "93.184.216.34:443", true},
	}
	for _, tc := range tests {
		t.Run("create "+tc.name, func(t *testing.T) {
			_, srv := newTestAPI(t)
			body := `{"kind":"http","method":"proxy","policy":{"proxy_target":"` + tc.target + `"}}`
			got := postStatus(t, srv, http.MethodPost, "/api/v1/endpoints", body)
			if tc.allowed && got == http.StatusBadRequest {
				t.Fatalf("target %q should pass validation, got 400", tc.target)
			}
			if !tc.allowed && got != http.StatusBadRequest {
				t.Fatalf("target %q should be rejected with 400, got %d", tc.target, got)
			}
		})
	}
}

func TestProxyEndpointPatchValidation(t *testing.T) {
	api, srv := newTestAPI(t)
	guard, err := proxy.NewGuard(false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	api.SetProxyGuard(guard)
	_ = api.data.CreateOrg(context.Background(), &store.Org{ID: defaultOrgID, Name: "default", CreatedAt: time.Now()})
	proxy.Register(context.Background(), api.data, api.conns, nil, guard)
	var ep endpointDTO
	do(t, srv, http.MethodPost, "/api/v1/endpoints", `{"kind":"http","method":"proxy","policy":{"proxy_target":"10.0.0.5:80"}}`, http.StatusCreated, &ep)

	api.SetProxyGuard(nil)
	do(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"policy":{"proxy_target":"169.254.169.254:80"}}`, http.StatusBadRequest, nil)
	do(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"policy":{"proxy_target":"10.0.0.6:80"}}`, http.StatusBadRequest, nil)
	do(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"policy":{"compression":true}}`, http.StatusBadRequest, nil)
	do(t, srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID, `{"policy":{"proxy_target":"93.184.216.34:443"}}`, http.StatusOK, nil)
}

func TestProxyEndpointOperatorOptIn(t *testing.T) {
	api, srv := newTestAPI(t)
	guard, err := proxy.NewGuard(false, false, []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	api.SetProxyGuard(guard)
	_ = api.data.CreateOrg(context.Background(), &store.Org{ID: defaultOrgID, Name: "default", CreatedAt: time.Now()})
	proxy.Register(context.Background(), api.data, api.conns, nil, guard)
	do(t, srv, http.MethodPost, "/api/v1/endpoints", `{"kind":"http","method":"proxy","policy":{"proxy_target":"10.2.3.4:80"}}`, http.StatusCreated, nil)
	do(t, srv, http.MethodPost, "/api/v1/endpoints", `{"kind":"http","method":"proxy","policy":{"proxy_target":"192.168.1.1:80"}}`, http.StatusBadRequest, nil)
}

func postStatus(t *testing.T, srv *httptest.Server, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
