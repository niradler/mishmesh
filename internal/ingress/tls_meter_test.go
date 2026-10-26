package ingress

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
)

type recordingMeter struct {
	mu   sync.Mutex
	in   map[string]int64
	out  map[string]int64
	done chan struct{}
}

func newRecordingMeter() *recordingMeter {
	return &recordingMeter{in: map[string]int64{}, out: map[string]int64{}, done: make(chan struct{}, 8)}
}

func (m *recordingMeter) AddBytes(kind string, in, out int64) {
	m.mu.Lock()
	m.in[kind] += in
	m.out[kind] += out
	m.mu.Unlock()
	m.done <- struct{}{}
}

func (m *recordingMeter) HTTPRequest(int) {}

func (m *recordingMeter) bytes(kind string) (int64, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.in[kind], m.out[kind]
}

type sinkAgentConn struct {
	received atomic.Int64
	closed   chan struct{}
}

func (*sinkAgentConn) AgentID() string { return "ag_1" }
func (*sinkAgentConn) Close() error    { return nil }
func (s *sinkAgentConn) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	near, far := net.Pipe()
	go func() {
		n, _ := io.Copy(io.Discard, far)
		s.received.Store(n)
		_ = far.Close()
		close(s.closed)
	}()
	return near, nil
}

func TestPassthroughMetersTheClientHelloAndFlow(t *testing.T) {
	tp, data := newPassthroughForTest(t, 2*time.Second)
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	ep := &store.Endpoint{ID: "ep_tls", AgentID: "ag_1", OrgID: "org_1", Kind: store.KindTLS, Subdomain: "secure", Lifecycle: store.LifecycleEphemeral, CreatedAt: time.Now()}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	conns := memory.NewConnStore()
	agent := &sinkAgentConn{closed: make(chan struct{})}
	conns.AddAgent(agent)
	conns.BindEndpoint(ep.ID, "ag_1")
	tp.conns = conns
	meter := newRecordingMeter()
	tp.meter = meter

	raw, err := net.Dial("tcp", tp.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := tls.Client(raw, &tls.Config{ServerName: "secure.example.com"})
	_ = raw.SetDeadline(time.Now().Add(400 * time.Millisecond))
	go func() {
		_ = client.Handshake()
		_ = raw.Close()
	}()

	select {
	case <-meter.done:
	case <-time.After(5 * time.Second):
		t.Fatal("flow was never metered")
	}
	<-agent.closed
	in, _ := meter.bytes(store.KindTLS)
	if in <= 0 || in != agent.received.Load() {
		t.Fatalf("metered in = %d, agent received %d, want equal and positive", in, agent.received.Load())
	}
	if got := conns.Usage("org_1"); got < in {
		t.Fatalf("org usage = %d, want >= %d", got, in)
	}
}
