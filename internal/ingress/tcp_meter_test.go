package ingress

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type echoAgentConn struct{ id string }

func (e *echoAgentConn) AgentID() string { return e.id }
func (e *echoAgentConn) Close() error    { return nil }

func (e *echoAgentConn) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	near, far := net.Pipe()
	go func() { _, _ = io.Copy(far, far) }()
	return near, nil
}

func TestTCPMetersBytesToEndpointOrg(t *testing.T) {
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "meter.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })

	if err := data.CreateOrg(ctx, &store.Org{ID: "org_meter", Name: "meter", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateAgent(ctx, &store.Agent{ID: "ag_meter", OrgID: "org_meter", Name: "m", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ep := &store.Endpoint{ID: "ep_meter", AgentID: "ag_meter", OrgID: "org_meter", Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, CreatedAt: time.Now()}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}

	conns := memory.NewConnStore()
	conns.AddAgent(&echoAgentConn{id: "ag_meter"})
	conns.BindEndpoint(ep.ID, "ag_meter")

	tcp := NewTCP(TCPOptions{Conns: conns, Data: data, Log: discardLogger(), BindHost: "127.0.0.1", PortMin: 24200, PortMax: 24300})
	t.Cleanup(tcp.Shutdown)
	port, err := tcp.Open(ep.ID, 0)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello-mishmesh")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	_ = conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if conns.Usage("org_meter") >= int64(len(payload)) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := conns.Usage("org_meter"); got < int64(len(payload)) {
		t.Fatalf("org usage = %d, want >= %d", got, len(payload))
	}
	if got := conns.Usage("org_other"); got != 0 {
		t.Fatalf("unrelated org usage = %d, want 0", got)
	}
}
