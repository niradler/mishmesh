package redis

import (
	"context"
	"net"
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/store"
)

type mockAgentConn struct {
	id string
}

func (m *mockAgentConn) AgentID() string { return m.id }
func (m *mockAgentConn) OpenStream(_ context.Context, _, _ string, _ map[string]string) (net.Conn, error) {
	return nil, nil
}
func (m *mockAgentConn) Close() error { return nil }

var _ store.AgentConn = (*mockAgentConn)(nil)

func newTestStore(t *testing.T) *ConnStore {
	t.Helper()
	redisURL := os.Getenv("MISHMESH_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("MISHMESH_TEST_REDIS_URL not set")
	}
	s, err := NewConnStore(redisURL)
	if err != nil {
		t.Fatalf("new conn store: %v", err)
	}
	t.Cleanup(func() { _ = s.rdb.Close() })
	return s
}

func newLocalStore(t *testing.T) *ConnStore {
	t.Helper()
	return newWithClient(goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0"}))
}

func TestLocalMapAddRemoveAgent(t *testing.T) {
	s := newLocalStore(t)

	conn1 := store.AgentConn(&mockAgentConn{id: "ag_1"})
	conn2 := store.AgentConn(&mockAgentConn{id: "ag_1"})

	superseded := s.AddAgent(conn1)
	if superseded != nil {
		t.Fatalf("first add: expected nil superseded, got %v", superseded)
	}

	got, ok := s.GetAgent("ag_1")
	if !ok || got != conn1 {
		t.Fatalf("get after add: ok=%v", ok)
	}

	superseded = s.AddAgent(conn2)
	if superseded != conn1 {
		t.Fatal("second add: expected conn1 superseded")
	}

	s.RemoveAgent(conn2)
	if _, ok := s.GetAgent("ag_1"); ok {
		t.Fatal("agent still present after remove")
	}
}

func TestLocalMapBindResolveUnbind(t *testing.T) {
	s := newLocalStore(t)

	conn := store.AgentConn(&mockAgentConn{id: "ag_2"})
	s.AddAgent(conn)

	s.BindEndpoint("ep_1", "ag_2")

	got, ok := s.ResolveEndpoint("ep_1")
	if !ok || got != conn {
		t.Fatalf("resolve: ok=%v got=%v", ok, got)
	}

	s.UnbindEndpoint("ep_1")
	if _, ok := s.ResolveEndpoint("ep_1"); ok {
		t.Fatal("endpoint still resolves after unbind")
	}
}

func TestLocalMapRemoveAgentClearsEndpoints(t *testing.T) {
	s := newLocalStore(t)

	conn := store.AgentConn(&mockAgentConn{id: "ag_3"})
	s.AddAgent(conn)
	s.BindEndpoint("ep_2", "ag_3")
	s.BindEndpoint("ep_3", "ag_3")

	s.RemoveAgent(conn)

	for _, ep := range []string{"ep_2", "ep_3"} {
		if _, ok := s.ResolveEndpoint(ep); ok {
			t.Fatalf("endpoint %s still resolves after agent removed", ep)
		}
	}
}

func TestRedisRoundTrip(t *testing.T) {
	s := newTestStore(t)

	conn := store.AgentConn(&mockAgentConn{id: "ag_redis_1"})
	s.AddAgent(conn)

	got, ok := s.GetAgent("ag_redis_1")
	if !ok || got != conn {
		t.Fatalf("get after add: ok=%v", ok)
	}

	s.RemoveAgent(conn)
	if _, ok := s.GetAgent("ag_redis_1"); ok {
		t.Fatal("agent still present after remove")
	}
}
