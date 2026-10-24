package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestRunStopsWhenUserLoginMissing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	a := New(Options{
		GatewayURL: srv.URL, Token: "tok", Out: io.Discard,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		UserToken: func(context.Context) (string, error) { return "", ErrLoginRequired },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Run(ctx); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want login required", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("gateway dialed %d times without a user token", hits.Load())
	}
}

func TestRunStopsWhenGatewayRejectsUserToken(t *testing.T) {
	var gotHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader.Store(r.Header.Get(tunnel.UserTokenHeader))
		http.Error(w, "invalid user token", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := New(Options{
		GatewayURL: srv.URL, Token: "tok", Out: io.Discard,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		UserToken: func(context.Context) (string, error) { return "user-tok", nil },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Run(ctx); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want login required", err)
	}
	if gotHeader.Load() != "user-tok" {
		t.Fatalf("user token header = %v", gotHeader.Load())
	}
}
