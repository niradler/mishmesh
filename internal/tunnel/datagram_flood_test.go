package tunnel

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type nopPacketConn struct{ net.PacketConn }

func (nopPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }

func TestDatagramListenerCapsFlowsByDefault(t *testing.T) {
	var drops atomic.Int64
	release := make(chan struct{})
	l := NewDatagramListener(nopPacketConn{}, DatagramListenerOptions{
		Open: func(net.Addr) (net.Conn, error) {
			<-release
			return nil, net.ErrClosed
		},
		OnDrop: func(string) { drops.Add(1) },
	})
	defer close(release)

	const flood = 5000
	for i := 0; i < flood; i++ {
		addr := &net.UDPAddr{IP: net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)), Port: 1000 + i%60000}
		l.deliver(addr, []byte("x"))
	}
	if got := l.ActiveFlows(); got > 1024 {
		t.Fatalf("flows = %d, want capped at 1024", got)
	}
	if drops.Load() == 0 {
		t.Fatal("expected flow_limit drops")
	}
}

func TestDatagramListenerOnDropRunsOutsideLock(t *testing.T) {
	var l *DatagramListener
	l = NewDatagramListener(nopPacketConn{}, DatagramListenerOptions{
		MaxFlows: 1,
		Open: func(net.Addr) (net.Conn, error) {
			time.Sleep(time.Second)
			return nil, net.ErrClosed
		},
		OnDrop: func(string) { l.ActiveFlows() },
	})
	done := make(chan struct{})
	go func() {
		l.deliver(&net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 1}, []byte("x"))
		l.deliver(&net.UDPAddr{IP: net.IPv4(1, 1, 1, 2), Port: 2}, []byte("x"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnDrop invoked while holding the listener lock")
	}
}

func BenchmarkRelayDatagramsSetup(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		streamA, streamB := net.Pipe()
		sockA, sockB := net.Pipe()
		done := make(chan struct{})
		go func() {
			RelayDatagrams(streamA, sockA, time.Minute)
			close(done)
		}()
		_ = streamB.Close()
		_ = sockB.Close()
		<-done
	}
}
