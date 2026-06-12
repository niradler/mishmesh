package controlplane

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func TestEnsureBootstrapIdempotent(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "boot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	const raw = "mm_test_bootstrap_token"

	id1, err := api.EnsureBootstrap(ctx, raw)
	if err != nil || id1 != bootstrapAgentID {
		t.Fatalf("first: id=%q err=%v", id1, err)
	}
	tok, err := data.GetTokenByHash(ctx, store.HashToken(raw))
	if err != nil || tok.AgentID != bootstrapAgentID {
		t.Fatalf("token lookup: %+v err=%v", tok, err)
	}

	id2, err := api.EnsureBootstrap(ctx, raw)
	if err != nil || id2 != bootstrapAgentID {
		t.Fatalf("second: id=%q err=%v", id2, err)
	}
	toks, err := data.ListTokensByAgent(ctx, bootstrapAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 1 {
		t.Fatalf("expected idempotent single token, got %d", len(toks))
	}
}
