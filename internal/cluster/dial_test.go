package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func startStallingListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	return ln.Addr().String()
}

func TestOpenHonoursContextWhileAnotherCallerIsDialing(t *testing.T) {
	addr := startStallingListener(t)
	client := NewClient(testSecret)
	client.dialTimeout = 2 * time.Second
	t.Cleanup(client.Close)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = client.Open(context.Background(), addr, "ag1", "ep", store.KindHTTP, nil)
	}()
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.Open(ctx, addr, "ag1", "ep", store.KindHTTP, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed > time.Second {
		t.Fatalf("second caller waited %v behind another caller's dial despite a 150ms deadline", elapsed)
	}
	<-firstDone
}

type gatedAgent struct {
	id       string
	inFlight atomic.Int64
	peak     atomic.Int64
	release  chan struct{}
}

func (a *gatedAgent) AgentID() string { return a.id }
func (a *gatedAgent) Close() error    { return nil }
func (a *gatedAgent) OpenStream(ctx context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	now := a.inFlight.Add(1)
	for {
		peak := a.peak.Load()
		if now <= peak || a.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer a.inFlight.Add(-1)
	select {
	case <-a.release:
	case <-ctx.Done():
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func TestServerCapsConcurrentStreamsPerSession(t *testing.T) {
	const limit = 4
	agent := &gatedAgent{id: "ag1", release: make(chan struct{})}
	srv := NewServer(ServerOptions{Secret: testSecret, Local: fixedResolver{"ag1": agent}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), MaxSessionStreams: limit})
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	client := NewClient(testSecret)
	t.Cleanup(client.Close)

	const attempts = 20
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_, _ = client.Open(ctx, addr.String(), "ag1", "ep", store.KindHTTP, nil)
		}()
	}
	wg.Wait()
	close(agent.release)
	if peak := agent.peak.Load(); peak > limit {
		t.Fatalf("peak concurrent stream handlers = %d, want <= %d", peak, limit)
	}
	if agent.peak.Load() == 0 {
		t.Fatal("no stream reached the agent")
	}
}
