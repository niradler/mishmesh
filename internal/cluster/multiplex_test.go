package cluster

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return c, err
}

func startCountingProxy(t *testing.T, target string) (string, *countingListener) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: inner}
	t.Cleanup(func() { _ = cl.Close() })
	go func() {
		for {
			c, err := cl.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
	return cl.Addr().String(), cl
}

func echoOnce(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	defer conn.Close()
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Errorf("write: %v", err)
		return
	}
	buf := make([]byte, len(payload))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Errorf("read: %v", err)
		return
	}
	if string(buf) != payload {
		t.Errorf("echo mismatch %q != %q", buf, payload)
	}
}

func TestRelayReusesOneConnectionPerPeer(t *testing.T) {
	agent := &echoAgent{id: "ag1"}
	srvAddr := startServer(t, fixedResolver{"ag1": agent}, nil)
	proxyAddr, counter := startCountingProxy(t, srvAddr)
	client := NewClient(testSecret)
	t.Cleanup(client.Close)
	remote := client.Remote("ag1", proxyAddr, nil)

	const sequential = 2000
	for i := 0; i < sequential; i++ {
		conn, err := remote.OpenStream(context.Background(), "ep", store.KindHTTP, nil)
		if err != nil {
			t.Fatalf("sequential open %d: %v", i, err)
		}
		echoOnce(t, conn, "ping")
	}

	const workers, perWorker = 50, 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				conn, err := remote.OpenStream(context.Background(), "ep", store.KindHTTP, nil)
				if err != nil {
					t.Errorf("concurrent open: %v", err)
					return
				}
				echoOnce(t, conn, "pong")
			}
		}()
	}
	wg.Wait()

	if got := counter.accepted.Load(); got != 1 {
		t.Fatalf("relay dialed %d TCP connections for %d streams, want 1", got, sequential+workers*perWorker)
	}
}

func TestRelayReconnectsAfterServerRestart(t *testing.T) {
	agent := &echoAgent{id: "ag1"}
	srv := NewServer(ServerOptions{Secret: testSecret, Local: fixedResolver{"ag1": agent}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(testSecret)
	t.Cleanup(client.Close)
	remote := client.Remote("ag1", addr.String(), nil)

	conn, err := remote.OpenStream(context.Background(), "ep", store.KindHTTP, nil)
	if err != nil {
		t.Fatal(err)
	}
	echoOnce(t, conn, "one")
	srv.Shutdown()

	srv2 := NewServer(ServerOptions{Secret: testSecret, Local: fixedResolver{"ag1": agent}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if _, err := srv2.Listen(addr.String()); err != nil {
		t.Skipf("could not rebind relay port: %v", err)
	}
	t.Cleanup(srv2.Shutdown)

	conn, err = remote.OpenStream(context.Background(), "ep", store.KindHTTP, nil)
	if err != nil {
		t.Fatalf("open after restart: %v", err)
	}
	echoOnce(t, conn, "two")
}
