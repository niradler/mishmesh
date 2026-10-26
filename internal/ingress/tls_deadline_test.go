package ingress

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newPassthroughForTest(t *testing.T, helloTimeout time.Duration) (*TLSPassthrough, store.DataStore) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "tls.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	tp := NewTLSPassthrough(TLSPassthroughOptions{
		Data:       data,
		Conns:      memory.NewConnStore(),
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		BaseDomain: "example.com",
	})
	tp.helloTimeout = helloTimeout
	if err := tp.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tp.Shutdown)
	return tp, data
}

func requireClosedByServer(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected the server to close the connection")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection was not closed within the hello deadline")
	}
}

func TestPassthroughClosesSilentConnection(t *testing.T) {
	tp, _ := newPassthroughForTest(t, 200*time.Millisecond)
	c, err := net.Dial("tcp", tp.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	requireClosedByServer(t, c)
}

func TestPassthroughClosesStalledHello(t *testing.T) {
	tp, _ := newPassthroughForTest(t, 200*time.Millisecond)
	c, err := net.Dial("tcp", tp.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte{0x16, 0x03, 0x01, 0x3f, 0xff})
	requireClosedByServer(t, c)
}

func TestReadClientHelloSNIRejectsOversizedRecord(t *testing.T) {
	_, _, err := readClientHelloSNI(bytes.NewReader([]byte{0x16, 0x03, 0x01, 0xff, 0xff}))
	if err == nil {
		t.Fatal("expected oversized record to be rejected")
	}
}
