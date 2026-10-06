package ingress

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestPathRoutingToggle(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "path.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	if err := data.CreateEndpoint(ctx, &store.Endpoint{ID: "ep_1", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP, Subdomain: "app", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		disable  bool
		host     string
		path     string
		wantCode int
	}{
		{"path routing on reaches endpoint", false, "example.com", "/tunnel/ep_1/x", http.StatusServiceUnavailable},
		{"path routing off is not found", true, "example.com", "/tunnel/ep_1/x", http.StatusNotFound},
		{"subdomain still works with path routing off", true, "app.example.com", "/x", http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := New(Options{
				Data:               data,
				Conns:              memory.NewConnStore(),
				Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
				BaseDomain:         "example.com",
				DisablePathRouting: tt.disable,
			})
			rec := httptest.NewRecorder()
			ing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+tt.host+tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}

func TestRewriteRequestStripsGateCredentials(t *testing.T) {
	tests := []struct {
		name       string
		policy     *store.EndpointPolicy
		cookie     string
		auth       string
		wantCookie string
		wantAuth   string
	}{
		{"oidc session cookie removed", nil, "mm_oidc=tok; app=1", "", "app=1", ""},
		{"only oidc cookie drops header", nil, "mm_oidc=tok", "", "", ""},
		{"state cookie removed", nil, "a=1; mm_oidc_state=n; b=2", "", "a=1; b=2", ""},
		{"similar cookie name kept", nil, "mm_oidc2=x; mm_oidc=tok", "", "mm_oidc2=x", ""},
		{"basic auth consumed is stripped", &store.EndpointPolicy{BasicAuthUser: "u", BasicAuthHash: "h"}, "", "Basic dTpw", "", ""},
		{"authorization kept without basic auth policy", nil, "", "Bearer abc", "", "Bearer abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseDomain: "example.com"})
			ep := &store.Endpoint{ID: "ep_1", Kind: store.KindHTTP, Policy: tt.policy}
			in := httptest.NewRequest(http.MethodGet, "http://app.example.com/x", nil)
			if tt.cookie != "" {
				in.Header.Set("Cookie", tt.cookie)
			}
			if tt.auth != "" {
				in.Header.Set("Authorization", tt.auth)
			}
			u := &upstream{ep: ep, outPath: "/x"}
			in = in.WithContext(context.WithValue(in.Context(), upstreamKey{}, u))
			pr := &httputil.ProxyRequest{In: in, Out: in.Clone(in.Context())}
			ing.rewriteRequest(pr)
			if got := pr.Out.Header.Get("Cookie"); got != tt.wantCookie {
				t.Errorf("Cookie = %q, want %q", got, tt.wantCookie)
			}
			if got := pr.Out.Header.Get("Authorization"); got != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tt.wantAuth)
			}
		})
	}
}
