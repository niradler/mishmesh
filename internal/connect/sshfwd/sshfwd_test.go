package sshfwd

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newTestStore(t *testing.T) store.DataStore {
	t.Helper()
	data, err := sqlite.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = data.Close() })
	return data
}

func seedAgent(t *testing.T, data store.DataStore) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := data.CreateOrg(ctx, &store.Org{ID: "org_default", Name: "default", CreatedAt: now}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	ag := &store.Agent{ID: store.NewID("ag"), OrgID: "org_default", Name: "tok", Status: store.AgentActive, CreatedAt: now}
	if err := data.CreateAgent(ctx, ag); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	raw, hash, err := store.GenerateToken()
	if err != nil {
		t.Fatalf("gen token: %v", err)
	}
	if err := data.CreateToken(ctx, &store.Token{ID: store.NewID("tok"), OrgID: "org_default", AgentID: ag.ID, Hash: hash, CreatedAt: now}); err != nil {
		t.Fatalf("create token: %v", err)
	}
	return raw
}

func TestRemoteForwardHTTPRoundTrip(t *testing.T) {
	data := newTestStore(t)
	conns := memory.NewConnStore()
	token := seedAgent(t, data)

	srv, err := New(Options{Data: data, Conns: conns, BaseDomain: "localhost:8080", PublicScheme: "http"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Shutdown()

	clientCfg := &ssh.ClientConfig{
		User:            "demo",
		Auth:            []ssh.AuthMethod{ssh.Password(token)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	fwd, err := client.Listen("tcp", "0.0.0.0:80")
	if err != nil {
		t.Fatalf("remote forward: %v", err)
	}
	defer fwd.Close()

	go func() {
		for {
			c, err := fwd.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 5)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write(buf)
			}(c)
		}
	}()

	ctx := context.Background()
	ep, err := data.GetEndpointBySubdomain(ctx, "demo")
	if err != nil {
		t.Fatalf("endpoint not registered: %v", err)
	}
	if ep.Method != store.MethodSSH || ep.Kind != store.KindHTTP {
		t.Fatalf("unexpected endpoint method=%s kind=%s", ep.Method, ep.Kind)
	}

	ac, ok := conns.ResolveEndpoint(ep.ID)
	if !ok {
		t.Fatalf("endpoint not bound in connection store")
	}
	stream, err := ac.OpenStream(ctx, ep.ID, store.KindHTTP, nil)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer stream.Close()

	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := make([]byte, 5)
	if _, err := io.ReadFull(stream, out); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(out) != "hello" {
		t.Fatalf("echo mismatch: got %q", out)
	}
}

func TestRejectsBadToken(t *testing.T) {
	data := newTestStore(t)
	conns := memory.NewConnStore()
	seedAgent(t, data)

	srv, err := New(Options{Data: data, Conns: conns, BaseDomain: "localhost:8080", PublicScheme: "http"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Shutdown()

	clientCfg := &ssh.ClientConfig{
		User:            "demo",
		Auth:            []ssh.AuthMethod{ssh.Password("mm_wrong_token")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	if _, err := ssh.Dial("tcp", ln.Addr().String(), clientCfg); err == nil {
		t.Fatalf("expected auth failure with bad token")
	}
}
