package cluster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

type echoAgent struct {
	id       string
	gotEP    string
	gotKind  string
	gotMeta  map[string]string
	openFail bool
}

func (e *echoAgent) AgentID() string { return e.id }

func (e *echoAgent) OpenStream(_ context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error) {
	if e.openFail {
		return nil, errors.New("boom")
	}
	e.gotEP, e.gotKind, e.gotMeta = endpointID, kind, meta
	server, client := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func (e *echoAgent) Close() error { return nil }

type fixedResolver map[string]store.AgentConn

func (f fixedResolver) LocalAgent(id string) (store.AgentConn, bool) {
	c, ok := f[id]
	return c, ok
}

func startServer(t *testing.T, local LocalResolver, now func() time.Time) string {
	t.Helper()
	srv := NewServer(ServerOptions{Secret: testSecret, Local: local, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: now})
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	return addr.String()
}

func rawExchange(t *testing.T, addr string, hdr Header) (status byte, closedWithoutStatus bool) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := WriteHeader(conn, hdr); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		return 0, true
	}
	return b[0], false
}

func TestRelayHeaderAuth(t *testing.T) {
	agent := &echoAgent{id: "ag1"}
	addr := startServer(t, fixedResolver{"ag1": agent}, nil)

	signed := func(mutate func(*Header), secret []byte) Header {
		h := Header{AgentID: "ag1", EndpointID: "ep1", Kind: store.KindHTTP, Meta: map[string]string{"a": "b"}, Timestamp: time.Now().Unix()}
		if mutate != nil {
			mutate(&h)
		}
		h.Sign(secret)
		return h
	}

	tests := []struct {
		name       string
		hdr        Header
		wantStatus byte
		wantClosed bool
	}{
		{"valid", signed(nil, testSecret), StatusOK, false},
		{"wrong secret", signed(nil, []byte("ffffffffffffffffffffffffffffffff")), 0, true},
		{"expired timestamp", signed(func(h *Header) { h.Timestamp = time.Now().Add(-2 * time.Minute).Unix() }, testSecret), 0, true},
		{"future timestamp", signed(func(h *Header) { h.Timestamp = time.Now().Add(2 * time.Minute).Unix() }, testSecret), 0, true},
		{"tampered agent", func() Header {
			h := signed(nil, testSecret)
			h.AgentID = "other"
			return h
		}(), 0, true},
		{"tampered meta", func() Header {
			h := signed(nil, testSecret)
			h.Meta = map[string]string{"a": "evil"}
			return h
		}(), 0, true},
		{"garbage mac", func() Header {
			h := signed(nil, testSecret)
			h.MAC = "zz"
			return h
		}(), 0, true},
		{"not here", signed(func(h *Header) { h.AgentID = "missing" }, testSecret), StatusNotHere, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, closed := rawExchange(t, addr, tt.hdr)
			if closed != tt.wantClosed {
				t.Fatalf("closedWithoutStatus = %v, want %v", closed, tt.wantClosed)
			}
			if !closed && status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
		})
	}
}

func TestRelayRejectsOversizedHeader(t *testing.T) {
	addr := startServer(t, fixedResolver{}, nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0x7f, 0xff, 0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(conn); err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("expected server to close connection, got %v", err)
	}
}

func TestRemoteConnRoundTrip(t *testing.T) {
	agent := &echoAgent{id: "ag1"}
	addr := startServer(t, fixedResolver{"ag1": agent}, nil)
	client := NewClient(testSecret)
	remote := client.Remote("ag1", addr, nil)

	conn, err := remote.OpenStream(context.Background(), "ep9", store.KindTCP, map[string]string{"target": "x:1"})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello-through-relay")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("hello-through-relay"))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, []byte("hello-through-relay")) {
		t.Fatalf("echo mismatch: %q", buf)
	}
	if agent.gotEP != "ep9" || agent.gotKind != store.KindTCP || agent.gotMeta["target"] != "x:1" {
		t.Fatalf("agent saw ep=%q kind=%q meta=%v", agent.gotEP, agent.gotKind, agent.gotMeta)
	}
}

func TestRemoteConnErrors(t *testing.T) {
	failing := &echoAgent{id: "bad", openFail: true}
	addr := startServer(t, fixedResolver{"bad": failing}, nil)
	client := NewClient(testSecret)

	t.Run("not here", func(t *testing.T) {
		_, err := client.Remote("nobody", addr, nil).OpenStream(context.Background(), "ep", store.KindHTTP, nil)
		if !errors.Is(err, ErrNotHere) {
			t.Fatalf("err = %v, want ErrNotHere", err)
		}
	})
	t.Run("open stream failure", func(t *testing.T) {
		_, err := client.Remote("bad", addr, nil).OpenStream(context.Background(), "ep", store.KindHTTP, nil)
		if !errors.Is(err, ErrRelayFailed) {
			t.Fatalf("err = %v, want ErrRelayFailed", err)
		}
	})
	t.Run("wrong secret", func(t *testing.T) {
		wrong := NewClient([]byte("ffffffffffffffffffffffffffffffff"))
		_, err := wrong.Remote("bad", addr, nil).OpenStream(context.Background(), "ep", store.KindHTTP, nil)
		if err == nil {
			t.Fatal("expected error with wrong secret")
		}
	})
	t.Run("dial failure", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dead := ln.Addr().String()
		_ = ln.Close()
		_, err = client.Remote("bad", dead, nil).OpenStream(context.Background(), "ep", store.KindHTTP, nil)
		if err == nil {
			t.Fatal("expected dial error")
		}
	})
	t.Run("close invokes callback", func(t *testing.T) {
		called := false
		_ = client.Remote("bad", addr, func() { called = true }).Close()
		if !called {
			t.Fatal("onClose not called")
		}
	})
}

func TestHeaderVerifyClockInjection(t *testing.T) {
	h := Header{AgentID: "a", Timestamp: 1000}
	h.Sign(testSecret)
	if err := h.Verify(testSecret, time.Unix(1030, 0)); err != nil {
		t.Fatalf("within skew: %v", err)
	}
	if err := h.Verify(testSecret, time.Unix(1000+61, 0)); !errors.Is(err, ErrStaleHeader) {
		t.Fatalf("err = %v, want ErrStaleHeader", err)
	}
	h.MAC = ""
	if err := h.Verify(testSecret, time.Unix(1000, 0)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}
