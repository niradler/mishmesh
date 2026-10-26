package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestEndpointStreamWaitsForRegistration(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	go func() {
		c, err := backend.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte("served"))
	}()

	a := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Out: io.Discard})
	registered := make(chan struct{})
	client, stream := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go a.handleStream(context.Background(), stream, tunnel.StreamInit{EndpointID: "ep-early", Kind: store.KindTCP}, registered)

	time.Sleep(100 * time.Millisecond)
	a.setTarget("ep-early", localTarget{addr: backend.Addr().String()})
	close(registered)

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len("served"))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("stream arriving before the register ack was dropped: %v", err)
	}
	if string(buf) != "served" {
		t.Fatalf("got %q", buf)
	}
}
