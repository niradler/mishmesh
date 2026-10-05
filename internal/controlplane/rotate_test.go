package controlplane

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

type fakeConn struct {
	id     string
	closed atomic.Bool
}

func (f *fakeConn) AgentID() string { return f.id }
func (f *fakeConn) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (f *fakeConn) Close() error {
	f.closed.Store(true)
	return nil
}

func TestRotateRevokesOldTokenAndDropsSession(t *testing.T) {
	f := newTenantFixture(t)
	conn := &fakeConn{id: f.agentA}
	f.api.conns.AddAgent(conn)

	var rotated struct {
		Token string `json:"token"`
	}
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/agents/"+f.agentA+"/rotate", "", http.StatusCreated, &rotated)
	if rotated.Token == "" || rotated.Token == f.tokenA {
		t.Fatalf("expected a fresh token, got %q", rotated.Token)
	}

	ctx := context.Background()
	if _, err := f.api.data.GetTokenByHash(ctx, store.HashToken(f.tokenA)); err == nil {
		t.Fatal("old token must no longer authenticate")
	}
	if _, err := f.api.data.GetTokenByHash(ctx, store.HashToken(rotated.Token)); err != nil {
		t.Fatalf("new token must authenticate: %v", err)
	}
	if !conn.closed.Load() {
		t.Fatal("live session using the old token must be closed")
	}

	var toks []tokenDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/agents/"+f.agentA+"/tokens", "", http.StatusOK, &toks)
	active := 0
	for _, tk := range toks {
		if tk.RevokedAt == nil {
			active++
		}
	}
	if len(toks) != 2 || active != 1 {
		t.Fatalf("want 2 tokens with 1 active, got %+v", toks)
	}
}
