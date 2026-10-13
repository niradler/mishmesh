package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDatagramFraming(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		wantErr error
	}{
		{"empty", []byte{}, nil},
		{"small", []byte("hello"), nil},
		{"max", bytes.Repeat([]byte("x"), MaxDatagramSize), nil},
		{"oversize", bytes.Repeat([]byte("x"), MaxDatagramSize+1), ErrDatagramTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WriteDatagram(&buf, tc.payload)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("write err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			out := make([]byte, MaxDatagramSize)
			n, err := ReadDatagram(&buf, out)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out[:n], tc.payload) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", n, len(tc.payload))
			}
		})
	}
}

func TestReadDatagramRejectsFrameLargerThanBuffer(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDatagram(&buf, []byte("too long")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDatagram(&buf, make([]byte, 3)); !errors.Is(err, ErrDatagramTooLarge) {
		t.Fatalf("err = %v, want ErrDatagramTooLarge", err)
	}
}

func udpEcho(t *testing.T) *net.UDPConn {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, MaxDatagramSize)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte("echo:"), buf[:n]...), addr)
		}
	}()
	return pc
}

func relayOpener(target string, idle time.Duration) func(net.Addr) (net.Conn, error) {
	return func(net.Addr) (net.Conn, error) {
		near, far := net.Pipe()
		socket, err := net.Dial("udp", target)
		if err != nil {
			return nil, err
		}
		go RelayDatagrams(far, socket, idle)
		return near, nil
	}
}

func startListener(t *testing.T, opts DatagramListenerOptions) (*DatagramListener, net.Addr) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := NewDatagramListener(pc, opts)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Serve()
	}()
	t.Cleanup(func() {
		_ = pc.Close()
		<-done
	})
	return l, pc.LocalAddr()
}

func exchange(c net.Conn, msg string) (string, error) {
	if _, err := c.Write([]byte(msg)); err != nil {
		return "", err
	}
	buf := make([]byte, 1024)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read reply to %q: %w", msg, err)
	}
	return string(buf[:n]), nil
}

func mustExchange(t *testing.T, c net.Conn, msg string) string {
	t.Helper()
	got, err := exchange(c, msg)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDatagramListenerRelaysFlowsIndependently(t *testing.T) {
	echo := udpEcho(t)
	var opened atomic.Int32
	inner := relayOpener(echo.LocalAddr().String(), time.Minute)
	_, addr := startListener(t, DatagramListenerOptions{
		Open: func(client net.Addr) (net.Conn, error) {
			opened.Add(1)
			return inner(client)
		},
	})

	clients := make([]net.Conn, 3)
	for i := range clients {
		c, err := net.Dial("udp", addr.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		clients[i] = c
	}
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Go(func() {
			for j := range 3 {
				msg := fmt.Sprintf("c%d-%d", i, j)
				if got, err := exchange(c, msg); err != nil || got != "echo:"+msg {
					t.Errorf("client %d: got %q, err %v", i, got, err)
				}
			}
		})
	}
	wg.Wait()
	if got := opened.Load(); got != int32(len(clients)) {
		t.Fatalf("opened %d streams, want one per client (%d)", got, len(clients))
	}
}

func TestDatagramListenerClosesIdleFlows(t *testing.T) {
	echo := udpEcho(t)
	done := make(chan [2]int64, 1)
	l, addr := startListener(t, DatagramListenerOptions{
		Open: relayOpener(echo.LocalAddr().String(), time.Minute),
		Idle: 100 * time.Millisecond,
		OnFlowDone: func(_ net.Addr, fromClient, toClient int64) {
			done <- [2]int64{fromClient, toClient}
		},
	})
	c, err := net.Dial("udp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := mustExchange(t, c, "ping"); got != "echo:ping" {
		t.Fatalf("got %q", got)
	}
	select {
	case counts := <-done:
		if counts != [2]int64{4, 9} {
			t.Fatalf("flow bytes = %v, want [4 9]", counts)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle flow was not closed")
	}
	if n := l.ActiveFlows(); n != 0 {
		t.Fatalf("active flows = %d, want 0", n)
	}
}

func TestDatagramListenerEnforcesFlowLimit(t *testing.T) {
	echo := udpEcho(t)
	var drops atomic.Int32
	_, addr := startListener(t, DatagramListenerOptions{
		Open:     relayOpener(echo.LocalAddr().String(), time.Minute),
		MaxFlows: 1,
		OnDrop: func(reason string) {
			if reason == DropFlowLimit {
				drops.Add(1)
			}
		},
	})
	first, _ := net.Dial("udp", addr.String())
	defer first.Close()
	if got := mustExchange(t, first, "a"); got != "echo:a" {
		t.Fatalf("got %q", got)
	}
	second, _ := net.Dial("udp", addr.String())
	defer second.Close()
	_, _ = second.Write([]byte("b"))
	deadline := time.Now().Add(2 * time.Second)
	for drops.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if drops.Load() == 0 {
		t.Fatal("second flow was not dropped at the flow limit")
	}
}

func TestRelayDatagramsClosesOnIdle(t *testing.T) {
	echo := udpEcho(t)
	near, far := net.Pipe()
	defer near.Close()
	socket, err := net.Dial("udp", echo.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		RelayDatagrams(far, socket, 50*time.Millisecond)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not stop after idle timeout")
	}
}
