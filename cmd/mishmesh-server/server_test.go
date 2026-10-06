package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler(), nil)
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", srv.IdleTimeout)
	}
	if srv.ReadTimeout != 0 {
		t.Errorf("ReadTimeout = %v, must stay unset to keep websocket and streaming bodies alive", srv.ReadTimeout)
	}
}

func TestNewHTTPServerClosesSlowHeaderClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer("", http.NotFoundHandler(), nil)
	srv.ReadHeaderTimeout = 150 * time.Millisecond
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	_, err = conn.Read(buf)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("server kept a slow-header connection open past ReadHeaderTimeout")
	}
}
