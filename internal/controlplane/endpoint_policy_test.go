package controlplane

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/store"
)

func statusAndBody(t *testing.T, srv *httptest.Server, method, path, body string) (int, string) {
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
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func newProxyTestAPI(t *testing.T) *httptest.Server {
	t.Helper()
	api, srv := newTestAPI(t)
	_ = api.data.CreateOrg(context.Background(), &store.Org{ID: defaultOrgID, Name: "default", CreatedAt: time.Now()})
	proxy.Register(context.Background(), api.data, api.conns, nil, nil)
	return srv
}

func TestCreateEndpointMethodAndPolicyValidation(t *testing.T) {
	validPin := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	tests := []struct {
		name     string
		body     string
		wantCode int
		wantMsg  string
	}{
		{"tailscale not implemented", `{"kind":"http","method":"tailscale"}`, http.StatusBadRequest, "method tailscale is not implemented"},
		{"cloudflare not implemented", `{"kind":"http","method":"cloudflare"}`, http.StatusBadRequest, "method cloudflare is not implemented"},
		{"unknown method", `{"kind":"http","method":"carrier-pigeon"}`, http.StatusBadRequest, "unknown method"},
		{"proxy without targets", `{"kind":"http","method":"proxy","policy":{}}`, http.StatusBadRequest, "proxy_targets required for proxy method"},
		{"proxy without policy", `{"kind":"http","method":"proxy"}`, http.StatusBadRequest, "proxy_targets required for proxy method"},
		{"proxy single target", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"]}}`, http.StatusCreated, ""},
		{"proxy https target", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["https://93.184.216.34:443"]}}`, http.StatusCreated, ""},
		{"proxy multiple targets failover", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["93.184.216.34:80","93.184.216.35:80"],"load_balance":"failover"}}`, http.StatusCreated, ""},
		{"proxy sticky", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["93.184.216.34:80","93.184.216.35:80"],"load_balance":"sticky"}}`, http.StatusCreated, ""},
		{"proxy round robin", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"load_balance":"round_robin"}}`, http.StatusCreated, ""},
		{"second target blocked", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["93.184.216.34:80","169.254.169.254:80"]}}`, http.StatusBadRequest, "blocked address"},
		{"target with path", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["https://93.184.216.34:443/x"]}}`, http.StatusBadRequest, "path or query"},
		{"bad load balance", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"],"load_balance":"random"}}`, http.StatusBadRequest, "load_balance"},
		{"bad ca pem", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["https://93.184.216.34:443"],"origin_tls":{"ca_pem":"junk"}}}`, http.StatusBadRequest, "ca_pem"},
		{"bad pin", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["https://93.184.216.34:443"],"origin_tls":{"pin_sha256":["abc"]}}}`, http.StatusBadRequest, "pin_sha256"},
		{"good pin", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["https://93.184.216.34:443"],"origin_tls":{"pin_sha256":["` + validPin + `"]}}}`, http.StatusCreated, ""},
		{"native with proxy targets", `{"kind":"http","method":"native","policy":{"proxy_targets":["93.184.216.34:80"]}}`, http.StatusBadRequest, "proxy_targets"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newProxyTestAPI(t)
			code, body := statusAndBody(t, srv, http.MethodPost, "/api/v1/endpoints", tc.body)
			if code != tc.wantCode {
				t.Fatalf("status %d want %d: %s", code, tc.wantCode, body)
			}
			if tc.wantMsg != "" && !strings.Contains(body, tc.wantMsg) {
				t.Fatalf("body %q missing %q", body, tc.wantMsg)
			}
		})
	}
}

func TestPatchEndpointPolicyValidation(t *testing.T) {
	srv := newProxyTestAPI(t)
	var ep endpointDTO
	do(t, srv, http.MethodPost, "/api/v1/endpoints", `{"kind":"http","method":"proxy","policy":{"proxy_targets":["93.184.216.34:80"]}}`, http.StatusCreated, &ep)
	path := "/api/v1/endpoints/" + ep.ID
	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"add target", `{"policy":{"proxy_targets":["93.184.216.34:80","93.184.216.35:80"]}}`, http.StatusOK},
		{"blocked target", `{"policy":{"proxy_targets":["93.184.216.34:80","10.0.0.1:80"]}}`, http.StatusBadRequest},
		{"empty targets", `{"policy":{"proxy_targets":[]}}`, http.StatusBadRequest},
		{"bad load balance", `{"policy":{"proxy_targets":["93.184.216.34:80"],"load_balance":"x"}}`, http.StatusBadRequest},
		{"bad origin tls", `{"policy":{"proxy_targets":["https://93.184.216.34:443"],"origin_tls":{"ca_pem":"junk"}}}`, http.StatusBadRequest},
		{"https target", `{"policy":{"proxy_targets":["https://93.184.216.34:443"],"origin_tls":{"insecure_skip_verify":true}}}`, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body := statusAndBody(t, srv, http.MethodPatch, path, tc.body)
			if code != tc.wantCode {
				t.Fatalf("status %d want %d: %s", code, tc.wantCode, body)
			}
		})
	}
}
