package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunStopsOnAuthRejection(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "401 token invalid", status: http.StatusUnauthorized, want: ErrTokenRejected},
		{name: "403 agent disabled", status: http.StatusForbidden, want: ErrAgentDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				http.Error(w, "nope", tt.status)
			}))
			t.Cleanup(srv.Close)

			a := New(Options{
				GatewayURL: srv.URL,
				Token:      "tok",
				Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
				Out:        io.Discard,
				Endpoints:  []EndpointSpec{{Kind: "http", LocalTarget: "127.0.0.1:1"}},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			start := time.Now()
			err := a.Run(ctx)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("took %v, expected no retry backoff", elapsed)
			}
			if hits.Load() != 1 {
				t.Fatalf("gateway hit %d times, want 1", hits.Load())
			}
		})
	}
}

func TestRunRetriesOnServerError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	a := New(Options{GatewayURL: srv.URL, Token: "tok", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Out: io.Discard})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := a.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded (retrying)", err)
	}
	if hits.Load() < 2 {
		t.Fatalf("hits = %d, want at least 2 retries", hits.Load())
	}
}

func TestRunFailsFastOnBadGatewayURL(t *testing.T) {
	a := New(Options{GatewayURL: "ftp://nope", Token: "tok", Out: io.Discard})
	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
		t.Fatalf("err = %v", err)
	}
}
