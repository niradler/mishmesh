package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type scriptedExitDialer struct {
	answers []string
	mu      sync.Mutex
	dialed  []string
}

func (d *scriptedExitDialer) ResolveAll(context.Context, string) ([]string, error) {
	return d.answers, nil
}

func (d *scriptedExitDialer) Dial(_ context.Context, target string, _ *store.OriginTLS, _ time.Duration) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, target)
	d.mu.Unlock()
	near, far := net.Pipe()
	go func() { _, _ = io.Copy(far, far) }()
	return near, nil
}

func (d *scriptedExitDialer) dials() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.dialed)
}

func exitAgent(dialer ExitDialer) *Agent {
	return New(Options{Egress: &EgressSpec{ServeExit: true}, ExitDialer: dialer, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func TestExitReportsResolvedTargetsAndDialsOnlyApproved(t *testing.T) {
	dialer := &scriptedExitDialer{answers: []string{"203.0.113.7:443", "203.0.113.8:443"}}
	server, exitSide := net.Pipe()
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		exitAgent(dialer).serveExit(context.Background(), exitSide, "svc.example.com:443", true)
	}()

	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	got, err := tunnel.ReadExitTargets(server)
	if err != nil || !slices.Equal(got, dialer.answers) {
		t.Fatalf("reported targets = %v, %v", got, err)
	}
	if err := tunnel.WriteExitTargets(server, []string{"203.0.113.8:443"}); err != nil {
		t.Fatal(err)
	}
	reply, err := tunnel.ReadEgressReply(server)
	if err != nil || reply.Status != tunnel.EgressOK {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	if d := dialer.dials(); !slices.Equal(d, []string{"203.0.113.8:443"}) {
		t.Fatalf("dialed %v, want only the approved target", d)
	}
	_ = server.Close()
	<-done
}

func TestExitRefusesTargetsItDidNotResolve(t *testing.T) {
	for _, approved := range [][]string{{"198.51.100.1:443"}, {"203.0.113.7:443", "198.51.100.1:443"}, {}} {
		dialer := &scriptedExitDialer{answers: []string{"203.0.113.7:443"}}
		server, exitSide := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			exitAgent(dialer).serveExit(context.Background(), exitSide, "svc.example.com:443", true)
			_ = exitSide.Close()
		}()
		_ = server.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := tunnel.ReadExitTargets(server); err != nil {
			t.Fatal(err)
		}
		if err := tunnel.WriteExitTargets(server, approved); err != nil {
			t.Fatal(err)
		}
		reply, err := tunnel.ReadEgressReply(server)
		if err != nil || reply.Status != tunnel.EgressInvalid {
			t.Fatalf("approved %v: reply = %+v, %v", approved, reply, err)
		}
		<-done
		_ = server.Close()
		if d := dialer.dials(); len(d) != 0 {
			t.Fatalf("approved %v: exit dialed %v", approved, d)
		}
	}
}

func TestExitDialsNothingWhenServerAborts(t *testing.T) {
	dialer := &scriptedExitDialer{answers: []string{"203.0.113.7:443"}}
	server, exitSide := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		exitAgent(dialer).serveExit(context.Background(), exitSide, "svc.example.com:443", true)
	}()
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := tunnel.ReadExitTargets(server); err != nil {
		t.Fatal(err)
	}
	_ = server.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("exit kept waiting after the server aborted")
	}
	if d := dialer.dials(); len(d) != 0 {
		t.Fatalf("exit dialed %v after the server aborted", d)
	}
}

func TestExitRefusalIsReportedBeforeTargets(t *testing.T) {
	server, exitSide := net.Pipe()
	defer server.Close()
	go exitAgent(failingExitDialer{}).serveExit(context.Background(), exitSide, "metadata.internal:80", true)
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	targets, reply, err := tunnel.ReadExitResolution(server)
	if err != nil || targets != nil || reply == nil || reply.Status != tunnel.EgressDenied || reply.Reason != "blocked_destination" {
		t.Fatalf("targets %v reply %+v err %v", targets, reply, err)
	}
}

type failingExitDialer struct{}

func (failingExitDialer) ResolveAll(context.Context, string) ([]string, error) {
	return nil, proxy.ErrBlocked
}

func (failingExitDialer) Dial(context.Context, string, *store.OriginTLS, time.Duration) (net.Conn, error) {
	return nil, proxy.ErrBlocked
}
