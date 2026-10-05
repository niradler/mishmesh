package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type flakyStore struct {
	store.DataStore
	failing atomic.Bool
}

var errDBDown = errors.New("db down")

func (f *flakyStore) GetSession(ctx context.Context, idHash string) (*store.Session, error) {
	if f.failing.Load() {
		return nil, errDBDown
	}
	return f.DataStore.GetSession(ctx, idHash)
}

func (f *flakyStore) GetUserByEmail(ctx context.Context, email string) (*store.User, error) {
	if f.failing.Load() {
		return nil, errDBDown
	}
	return f.DataStore.GetUserByEmail(ctx, email)
}

func TestStoreFailureIsNotReportedAsAuthFailure(t *testing.T) {
	inner, err := sqlite.Open(filepath.Join(t.TempDir(), "flaky.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	data := &flakyStore{DataStore: inner}
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("localhost:8080", "http")
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	doc(t, client, srv, http.MethodPost, "/api/v1/auth/register", `{"email":"o@example.com","password":"supersecret","name":"O"}`, http.StatusCreated, nil)
	doc(t, client, srv, http.MethodGet, "/api/v1/auth/me", "", http.StatusOK, nil)

	data.failing.Store(true)
	doc(t, client, srv, http.MethodGet, "/api/v1/agents", "", http.StatusServiceUnavailable, nil)
	doc(t, &http.Client{}, srv, http.MethodPost, "/api/v1/auth/login", `{"email":"o@example.com","password":"supersecret"}`, http.StatusServiceUnavailable, nil)

	data.failing.Store(false)
	doc(t, client, srv, http.MethodGet, "/api/v1/agents", "", http.StatusOK, nil)
	doc(t, &http.Client{}, srv, http.MethodPost, "/api/v1/auth/login", `{"email":"nobody@example.com","password":"x"}`, http.StatusUnauthorized, nil)
	doc(t, &http.Client{}, srv, http.MethodGet, "/api/v1/agents", "", http.StatusUnauthorized, nil)
}
