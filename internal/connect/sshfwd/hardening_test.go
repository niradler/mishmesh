package sshfwd

import (
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
)

func startServer(t *testing.T, opts Options) (*Server, net.Listener, store.DataStore, string) {
	t.Helper()
	data := newTestStore(t)
	token := seedAgent(t, data)
	opts.Data = data
	opts.Conns = memory.NewConnStore()
	opts.BaseDomain = "localhost:8080"
	opts.PublicScheme = "http"
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	return srv, ln, data, token
}

func expectServerCloses(t *testing.T, c net.Conn, within time.Duration, msg string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	buf := make([]byte, 4096)
	for {
		if _, err := c.Read(buf); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal(msg)
			}
			return
		}
	}
}

func clientConfig(pass string) *ssh.ClientConfig {
	return &ssh.ClientConfig{User: "demo", Auth: []ssh.AuthMethod{ssh.Password(pass)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}
}

func TestIdleHandshakeClosedAfterDeadline(t *testing.T) {
	_, ln, _, _ := startServer(t, Options{HandshakeTimeout: 200 * time.Millisecond})
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	expectServerCloses(t, c, 3*time.Second, "idle client was not closed by the server")
}

func TestHandshakeCapRejectsExcessConnections(t *testing.T) {
	_, ln, _, _ := startServer(t, Options{MaxHandshakes: 2, HandshakeTimeout: 5 * time.Second})
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.Close()
	}
	time.Sleep(100 * time.Millisecond)
	extra, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer extra.Close()
	expectServerCloses(t, extra, 2*time.Second, "connection beyond the handshake cap was not closed")
}

func TestAuthFailuresThrottledPerIP(t *testing.T) {
	_, ln, _, token := startServer(t, Options{AuthFailureLimit: 3, AuthFailureWindow: time.Minute})
	for i := 0; i < 3; i++ {
		if _, err := ssh.Dial("tcp", ln.Addr().String(), clientConfig("wrong")); err == nil {
			t.Fatalf("expected auth failure")
		}
	}
	if c, err := ssh.Dial("tcp", ln.Addr().String(), clientConfig(token)); err == nil {
		_ = c.Close()
		t.Fatalf("valid token accepted after throttle limit")
	}
}

func TestShutdownClosesLiveSessions(t *testing.T) {
	srv, ln, _, token := startServer(t, Options{})
	client, err := ssh.Dial("tcp", ln.Addr().String(), clientConfig(token))
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()
	srv.Shutdown()
	done := make(chan struct{})
	go func() {
		_ = client.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("live session survived Shutdown")
	}
}
