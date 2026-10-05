package tunnel

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func tcpPair(t *testing.T) (client, server *net.TCPConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan *net.TCPConn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c.(*net.TCPConn)
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c.(*net.TCPConn), <-accepted
}

func TestSpliceHalfCloseDeliversReplyAndCountsBytes(t *testing.T) {
	cases := []struct {
		name    string
		request string
		reply   string
	}{
		{"short", "hi", "banner|echo:hi"},
		{"empty request", "", "banner|echo:"},
		{"large", string(bytes.Repeat([]byte("r"), 1<<20)), "banner|echo:" + string(bytes.Repeat([]byte("r"), 1<<20))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientEnd, spliceClientSide := tcpPair(t)
			spliceBackendSide, backendEnd := tcpPair(t)

			go func() {
				defer backendEnd.Close()
				_, _ = backendEnd.Write([]byte("banner|"))
				in, _ := io.ReadAll(backendEnd)
				_, _ = backendEnd.Write([]byte("echo:" + string(in)))
			}()

			type result struct{ up, down int64 }
			resCh := make(chan result, 1)
			go func() {
				up, down := Splice(spliceClientSide, spliceBackendSide)
				resCh <- result{up, down}
			}()

			go func() {
				_, _ = clientEnd.Write([]byte(tc.request))
				_ = clientEnd.CloseWrite()
			}()
			_ = clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
			got, err := io.ReadAll(clientEnd)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.reply {
				t.Fatalf("reply len %d, want %d", len(got), len(tc.reply))
			}
			select {
			case res := <-resCh:
				if res.up != int64(len(tc.request)) || res.down != int64(len(tc.reply)) {
					t.Fatalf("counts up=%d down=%d, want %d/%d", res.up, res.down, len(tc.request), len(tc.reply))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("splice did not finish")
			}
		})
	}
}

func TestSpliceFallsBackToFullCloseWithoutHalfClose(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		Splice(a2, b1)
		close(done)
	}()
	go func() { _, _ = io.Copy(io.Discard, b2) }()
	_, _ = a1.Write([]byte("x"))
	_ = a1.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("splice hung on a conn without CloseWrite")
	}
	_ = b2.Close()
}

func TestSpliceIdleHalfClosedConnectionIsTornDown(t *testing.T) {
	prev := halfCloseIdleTimeout
	halfCloseIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { halfCloseIdleTimeout = prev })

	clientEnd, spliceClientSide := tcpPair(t)
	spliceBackendSide, backendEnd := tcpPair(t)
	_ = backendEnd

	done := make(chan struct{})
	go func() {
		Splice(spliceClientSide, spliceBackendSide)
		close(done)
	}()
	_ = clientEnd.CloseWrite()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("idle half-closed splice was never torn down")
	}
}

func TestStreamErrorRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteStreamError(&buf, "connection refused"); err != nil {
		t.Fatal(err)
	}
	reason, ok := ReadStreamError(bufio.NewReader(&buf))
	if !ok || reason != "connection refused" {
		t.Fatalf("reason=%q ok=%v", reason, ok)
	}
}

func TestReadStreamErrorLeavesRegularDataUntouched(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"http response", "HTTP/1.1 200 OK\r\n\r\n"},
		{"short", "HT"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br := bufio.NewReader(bytes.NewBufferString(tc.in))
			if _, ok := ReadStreamError(br); ok {
				t.Fatal("regular data parsed as stream error")
			}
			rest, _ := io.ReadAll(br)
			if string(rest) != tc.in {
				t.Fatalf("data consumed: %q", rest)
			}
		})
	}
}
