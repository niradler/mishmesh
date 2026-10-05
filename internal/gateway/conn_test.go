package gateway

import (
	"net"
	"sync/atomic"
	"testing"
)

func TestTrackedConnReportsCloseExactlyOnce(t *testing.T) {
	var closed atomic.Int32
	near, far := net.Pipe()
	defer far.Close()
	c := &trackedConn{Conn: near, onClose: func() { closed.Add(1) }}

	for i := 0; i < 3; i++ {
		_ = c.Close()
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("onClose ran %d times, want 1", got)
	}
}
