package ingress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type lookupFaultStore struct {
	store.DataStore
	err error
}

func (f *lookupFaultStore) GetEndpointBySubdomain(ctx context.Context, sub string) (*store.Endpoint, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.DataStore.GetEndpointBySubdomain(ctx, sub)
}

func (f *lookupFaultStore) GetEndpointByDomain(ctx context.Context, d string) (*store.Endpoint, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.DataStore.GetEndpointByDomain(ctx, d)
}

func (f *lookupFaultStore) GetEndpoint(ctx context.Context, id string) (*store.Endpoint, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.DataStore.GetEndpoint(ctx, id)
}

func TestResolveSeparatesNotFoundFromStoreFailure(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	if err := data.CreateEndpoint(ctx, &store.Endpoint{ID: "ep_1", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindHTTP, Lifecycle: store.LifecycleEphemeral, Subdomain: "app", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		host      string
		path      string
		dbErr     error
		wantCode  int
		wantRetry bool
	}{
		{"unknown subdomain", "ghost.example.com", "/", nil, http.StatusNotFound, false},
		{"known subdomain without agent", "app.example.com", "/", nil, http.StatusServiceUnavailable, false},
		{"subdomain db error", "app.example.com", "/", errors.New("too many clients"), http.StatusServiceUnavailable, true},
		{"custom domain db error", "tunnel.corp.test", "/", errors.New("too many clients"), http.StatusServiceUnavailable, true},
		{"custom domain unknown", "tunnel.corp.test", "/", nil, http.StatusNotFound, false},
		{"path id db error", "example.com", "/tunnel/ep_1/x", errors.New("timeout"), http.StatusServiceUnavailable, true},
		{"path id unknown", "example.com", "/tunnel/nope/x", nil, http.StatusNotFound, false},
		{"no route", "example.com", "/", nil, http.StatusNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ing := New(Options{
				Data:       &lookupFaultStore{DataStore: data, err: tt.dbErr},
				Conns:      memory.NewConnStore(),
				Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
				BaseDomain: "example.com",
			})
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+tt.path, nil)
			rec := httptest.NewRecorder()
			ing.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After") != ""; got != tt.wantRetry {
				t.Fatalf("Retry-After present = %v, want %v", got, tt.wantRetry)
			}
		})
	}
}
