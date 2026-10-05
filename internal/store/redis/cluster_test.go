package redis

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/cluster"
	"github.com/mishmesh/mishmesh/internal/store"
)

var clusterSecret = []byte("0123456789abcdef0123456789abcdef")

type fakeAgent struct {
	id      string
	closed  atomic.Bool
	virtual bool
	tag     string
}

func (f *fakeAgent) AgentID() string { return f.id }
func (f *fakeAgent) OpenStream(_ context.Context, endpointID, _ string, _ map[string]string) (net.Conn, error) {
	server, client := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = server.Write([]byte(f.tag + ":" + endpointID))
	}()
	return client, nil
}
func (f *fakeAgent) Close() error       { f.closed.Store(true); return nil }
func (f *fakeAgent) ClusterLocal() bool { return f.virtual }

type testNode struct {
	cs    *ClusterConnStore
	relay *cluster.Server
	addr  string
}

func newNode(t *testing.T, mr *miniredis.Miniredis, name string, ttl time.Duration) *testNode {
	t.Helper()
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n := &testNode{}
	cs, err := newClusterWithClient(context.Background(), rdb, ClusterOptions{
		NodeID:    name,
		Advertise: "placeholder",
		Relay:     cluster.NewClient(clusterSecret),
		TTL:       ttl,
		Log:       log,
	})
	if err != nil {
		t.Fatal(err)
	}
	relay := cluster.NewServer(cluster.ServerOptions{Secret: clusterSecret, Local: cs, Log: log})
	addr, err := relay.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cs.selfValue = mustOwnerJSON(name, addr.String())
	n.cs, n.relay, n.addr = cs, relay, addr.String()
	t.Cleanup(func() { relay.Shutdown(); _ = cs.Close() })
	return n
}

func mustOwnerJSON(node, addr string) string {
	return `{"node":"` + node + `","addr":"` + addr + `"}`
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readAll(t *testing.T, c net.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return string(b)
}

func TestClusterResolvesRemoteAgentThroughRelay(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)

	agent := &fakeAgent{id: "ag1", tag: "from-a"}
	a.cs.AddAgent(agent)
	a.cs.BindEndpoint("ep1", "ag1")

	if _, ok := a.cs.GetAgent("ag1"); !ok {
		t.Fatal("owner should resolve agent locally")
	}

	conn, ok := b.cs.ResolveEndpoint("ep1")
	if !ok {
		t.Fatal("node b could not resolve endpoint bound on node a")
	}
	stream, err := conn.OpenStream(context.Background(), "ep1", store.KindHTTP, nil)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer stream.Close()
	if got := readAll(t, stream); got != "from-a:ep1" {
		t.Fatalf("stream payload = %q", got)
	}

	if _, ok := b.cs.GetAgent("ag1"); !ok {
		t.Fatal("node b GetAgent should find remote agent")
	}
	if _, ok := b.cs.GetAgent("nobody"); ok {
		t.Fatal("unknown agent must not resolve")
	}
	if _, ok := b.cs.ResolveEndpoint("unknown-ep"); ok {
		t.Fatal("unknown endpoint must not resolve")
	}
	if b.cs.OwnedElsewhere("ag1") != true || a.cs.OwnedElsewhere("ag1") != false {
		t.Fatal("OwnedElsewhere mismatch")
	}
}

func TestClusterUnbindEndpoint(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)
	a.cs.AddAgent(&fakeAgent{id: "ag1"})
	a.cs.BindEndpoint("ep1", "ag1")
	if _, ok := b.cs.ResolveEndpoint("ep1"); !ok {
		t.Fatal("expected resolve")
	}
	b.cs.UnbindEndpoint("ep1")
	if _, ok := b.cs.ResolveEndpoint("ep1"); ok {
		t.Fatal("endpoint should be unbound cluster-wide")
	}
}

func TestClusterSupersedeKicksOldNode(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)

	oldSession := &fakeAgent{id: "ag1", tag: "a"}
	a.cs.AddAgent(oldSession)
	a.cs.BindEndpoint("ep1", "ag1")

	newSession := &fakeAgent{id: "ag1", tag: "b"}
	if superseded := b.cs.AddAgent(newSession); superseded != nil {
		t.Fatal("no local session on b to supersede")
	}
	b.cs.BindEndpoint("ep1", "ag1")

	eventually(t, "node a to close stale session", oldSession.closed.Load)
	if newSession.closed.Load() {
		t.Fatal("new session must stay open")
	}

	a.cs.RemoveAgent(oldSession)
	if !a.cs.OwnedElsewhere("ag1") {
		t.Fatal("agent should be owned by b after a removed its stale session")
	}
	conn, ok := a.cs.ResolveEndpoint("ep1")
	if !ok {
		t.Fatal("endpoint must survive the stale session cleanup on node a")
	}
	stream, err := conn.OpenStream(context.Background(), "ep1", store.KindHTTP, nil)
	if err != nil {
		t.Fatalf("open via relay to b: %v", err)
	}
	defer stream.Close()
	if got := readAll(t, stream); got != "b:ep1" {
		t.Fatalf("payload = %q, want served by b", got)
	}
}

func TestClusterKickIgnoredWhenAgentReturnedToNode(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)

	first := &fakeAgent{id: "ag1"}
	a.cs.AddAgent(first)
	b.cs.AddAgent(&fakeAgent{id: "ag1"})
	eventually(t, "first session kicked", first.closed.Load)

	returned := &fakeAgent{id: "ag1"}
	a.cs.AddAgent(returned)
	a.cs.handleKick(`{"agent_id":"ag1","node":"node-a","new_node":"node-b"}`)
	if returned.closed.Load() {
		t.Fatal("stale kick must not close a session that re-owns the agent")
	}
}

func TestClusterRemoveAgentOnlyDeletesOwnKey(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)

	sa := &fakeAgent{id: "ag1"}
	a.cs.AddAgent(sa)
	b.cs.AddAgent(&fakeAgent{id: "ag1"})
	a.cs.RemoveAgent(sa)
	if !mr.Exists(agentKeyPrefix + "ag1") {
		t.Fatal("a must not delete the key now owned by b")
	}

	sb, _ := b.cs.LocalAgent("ag1")
	b.cs.RemoveAgent(sb)
	if mr.Exists(agentKeyPrefix + "ag1") {
		t.Fatal("owner removal should delete the key")
	}
}

func TestClusterRemoveAgentClearsEndpointBindings(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	sa := &fakeAgent{id: "ag1"}
	a.cs.AddAgent(sa)
	a.cs.BindEndpoint("ep1", "ag1")
	if !mr.Exists(endpointKeyPrefix + "ep1") {
		t.Fatal("binding should be written")
	}
	a.cs.RemoveAgent(sa)
	if mr.Exists(endpointKeyPrefix + "ep1") {
		t.Fatal("binding should be removed with the owning agent")
	}
}

func TestClusterHeartbeatRefreshesAndReclaims(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 30*time.Second)
	a.cs.AddAgent(&fakeAgent{id: "ag1"})
	a.cs.BindEndpoint("ep1", "ag1")

	mr.FastForward(25 * time.Second)
	a.cs.heartbeat(context.Background())
	mr.FastForward(25 * time.Second)
	if !mr.Exists(agentKeyPrefix+"ag1") || !mr.Exists(endpointKeyPrefix+"ep1") {
		t.Fatal("heartbeat should keep keys alive past the original ttl")
	}

	mr.Del(agentKeyPrefix + "ag1")
	a.cs.heartbeat(context.Background())
	if !mr.Exists(agentKeyPrefix + "ag1") {
		t.Fatal("heartbeat should re-claim a lost key")
	}
}

func TestClusterKeysExpireWithoutHeartbeat(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 30*time.Second)
	b := newNode(t, mr, "node-b", 30*time.Second)
	a.cs.AddAgent(&fakeAgent{id: "ag1"})
	a.cs.BindEndpoint("ep1", "ag1")
	mr.FastForward(31 * time.Second)
	if _, ok := b.cs.ResolveEndpoint("ep1"); ok {
		t.Fatal("a crashed owner's bindings must expire")
	}
}

func TestClusterHeartbeatClosesSessionOwnedElsewhere(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	stale := &fakeAgent{id: "ag1"}
	a.cs.AddAgent(stale)
	mr.Set(agentKeyPrefix+"ag1", mustOwnerJSON("node-b", "10.0.0.2:7443"))
	a.cs.heartbeat(context.Background())
	if !stale.closed.Load() {
		t.Fatal("heartbeat should close a session another node owns")
	}
}

func TestClusterVirtualAgentStaysLocal(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)
	a.cs.AddAgent(&fakeAgent{id: "ag_proxy", virtual: true, tag: "proxy-a"})
	b.cs.AddAgent(&fakeAgent{id: "ag_proxy", virtual: true, tag: "proxy-b"})
	if mr.Exists(agentKeyPrefix + "ag_proxy") {
		t.Fatal("virtual agents must not be registered in redis")
	}

	a.cs.BindEndpoint("epx", "ag_proxy")
	conn, ok := b.cs.ResolveEndpoint("epx")
	if !ok {
		t.Fatal("virtual agent endpoint should resolve on every node")
	}
	stream, err := conn.OpenStream(context.Background(), "epx", store.KindHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if got := readAll(t, stream); got != "proxy-b:epx" {
		t.Fatalf("virtual agent must be served locally, got %q", got)
	}
	mr.FastForward(10 * time.Minute)
	if _, ok := b.cs.ResolveEndpoint("epx"); !ok {
		t.Fatal("virtual agent bindings must not expire")
	}
}

func TestClusterForceKickViaRemoteClose(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)
	session := &fakeAgent{id: "ag1"}
	a.cs.AddAgent(session)

	remote, ok := b.cs.GetAgent("ag1")
	if !ok {
		t.Fatal("expected remote agent")
	}
	_ = remote.Close()
	eventually(t, "forced close on owner", session.closed.Load)
}

func TestClusterDrainRemovesOwnership(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	session := &fakeAgent{id: "ag1"}
	a.cs.AddAgent(session)
	a.cs.BindEndpoint("ep1", "ag1")

	a.cs.Drain(context.Background())
	if !session.closed.Load() {
		t.Fatal("drain should close local sessions")
	}
	if mr.Exists(agentKeyPrefix + "ag1") {
		t.Fatal("drain should remove ownership key")
	}
	if _, ok := a.cs.LocalAgent("ag1"); ok {
		t.Fatal("drain should clear local sessions")
	}
}

func TestPortClaims(t *testing.T) {
	mr := miniredis.RunT(t)
	a := newNode(t, mr, "node-a", 0)
	b := newNode(t, mr, "node-b", 0)
	ca := a.cs.PortClaims(20000, 20002)
	cb := b.cs.PortClaims(20000, 20002)
	ctx := context.Background()

	p1, err := ca.Claim(ctx, "ep1", 0)
	if err != nil || p1 != 20000 {
		t.Fatalf("claim 1 = %d, %v", p1, err)
	}
	again, err := cb.Claim(ctx, "ep1", 0)
	if err != nil || again != 20000 {
		t.Fatalf("claim is idempotent per endpoint: %d, %v", again, err)
	}
	p2, err := cb.Claim(ctx, "ep2", 0)
	if err != nil || p2 != 20001 {
		t.Fatalf("claim 2 = %d, %v", p2, err)
	}
	if _, err := ca.Claim(ctx, "ep3", 20001); err == nil {
		t.Fatal("explicit port already claimed must fail")
	}
	if _, err := ca.Claim(ctx, "ep3", 30000); err == nil {
		t.Fatal("port outside range must fail")
	}
	p3, err := ca.Claim(ctx, "ep3", 0)
	if err != nil || p3 != 20002 {
		t.Fatalf("claim 3 = %d, %v", p3, err)
	}
	if _, err := ca.Claim(ctx, "ep4", 0); err == nil {
		t.Fatal("exhausted range must fail")
	}

	if id, ok := cb.Lookup(ctx, 20000); !ok || id != "ep1" {
		t.Fatalf("lookup = %q, %v", id, ok)
	}
	if err := cb.Release(ctx, "ep1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := ca.Lookup(ctx, 20000); ok {
		t.Fatal("released port must be free")
	}
	p4, err := ca.Claim(ctx, "ep4", 0)
	if err != nil || p4 != 20000 {
		t.Fatalf("reclaim = %d, %v", p4, err)
	}
	if err := ca.Release(ctx, "never-claimed"); err != nil {
		t.Fatalf("release of unknown endpoint: %v", err)
	}
}
