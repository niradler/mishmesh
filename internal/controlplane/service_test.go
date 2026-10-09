package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type bootstrapRaceStore struct {
	store.DataStore
	agentLookups atomic.Int32
	agentsReady  sync.WaitGroup
	tokensReady  sync.WaitGroup
}

func (s *bootstrapRaceStore) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	agent, err := s.DataStore.GetAgent(ctx, id)
	if id == bootstrapAgentID && errors.Is(err, store.ErrNotFound) && s.agentLookups.Add(1) <= 2 {
		s.agentsReady.Done()
		s.agentsReady.Wait()
	}
	return agent, err
}

func (s *bootstrapRaceStore) CreateToken(ctx context.Context, token *store.Token) error {
	s.tokensReady.Done()
	s.tokensReady.Wait()
	return s.DataStore.CreateToken(ctx, token)
}

func TestEnsureBootstrapConcurrent(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "boot-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	if _, err := New(data, nil, "", nil).ensureOrg(ctx, defaultOrgID); err != nil {
		t.Fatal(err)
	}
	racing := &bootstrapRaceStore{DataStore: data}
	racing.agentsReady.Add(2)
	racing.tokensReady.Add(2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			id, err := New(racing, nil, "", nil).EnsureBootstrap(ctx, "concurrent-bootstrap-token")
			if err == nil && id != bootstrapAgentID {
				err = errors.New("unexpected bootstrap agent")
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	tokens, err := data.ListTokensByAgent(ctx, bootstrapAgentID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("expected one shared bootstrap token, got %d: %v", len(tokens), err)
	}
}

func TestEnsureBootstrapIdempotent(t *testing.T) {
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "boot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
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
