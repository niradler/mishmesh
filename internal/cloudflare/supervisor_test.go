package cloudflare

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	helperEnv     = "MISHMESH_CF_HELPER"
	helperOutEnv  = "MISHMESH_CF_HELPER_OUT"
	helperExitEnv = "MISHMESH_CF_HELPER_EXIT"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		runHelperProcess()
		return
	}
	os.Exit(m.Run())
}

func runHelperProcess() {
	if out := os.Getenv(helperOutEnv); out != "" {
		record := "token=" + os.Getenv("TUNNEL_TOKEN") + "\nargs=" + strings.Join(os.Args[1:], " ") + "\n"
		f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(record)
			f.Close()
		}
	}
	if os.Getenv(helperExitEnv) == "1" {
		os.Exit(3)
	}
	addr := ""
	for i, a := range os.Args {
		if a == "--metrics" && i+1 < len(os.Args) {
			addr = os.Args[i+1]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Addr: addr, Handler: mux}
	_ = srv.ListenAndServe()
	os.Exit(4)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func newTestSupervisor(t *testing.T, exitImmediately bool) (*Supervisor, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	t.Setenv(helperEnv, "1")
	t.Setenv(helperOutEnv, out)
	if exitImmediately {
		t.Setenv(helperExitEnv, "1")
	}
	sup := NewSupervisor(SupervisorConfig{Bin: os.Args[0], Token: "  secret-token-value\n", MetricsAddr: freeAddr(t)}, nil)
	return sup, out
}

func TestSupervisorRunsReadyAndStops(t *testing.T) {
	sup, out := newTestSupervisor(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(ctx)
	}()
	waitFor(t, func() bool { return sup.Status().Ready })
	st := sup.Status()
	if !st.Running || st.PID == 0 || st.Restarts != 0 {
		t.Fatalf("status = %+v", st)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	record := string(raw)
	if !strings.Contains(record, "token=secret-token-value\n") {
		t.Fatalf("token not passed via environment: %q", record)
	}
	argsLine := record[strings.Index(record, "args="):]
	if strings.Contains(argsLine, "secret-token-value") {
		t.Fatalf("token leaked into argv: %q", argsLine)
	}
	for _, want := range []string{"tunnel", "--no-autoupdate", "--metrics " + sup.cfg.MetricsAddr, "run"} {
		if !strings.Contains(argsLine, want) {
			t.Fatalf("args %q missing %q", argsLine, want)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	if st := sup.Status(); st.Running || st.Ready {
		t.Fatalf("status after stop = %+v", st)
	}
}

func TestSupervisorRestartsWithBackoff(t *testing.T) {
	sup, _ := newTestSupervisor(t, true)
	sup.minBackoff = 5 * time.Millisecond
	sup.maxBackoff = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(ctx)
	}()
	waitFor(t, func() bool { return sup.Status().Restarts >= 3 })
	st := sup.Status()
	if !strings.Contains(st.LastExit, "exit status 3") || st.Ready {
		t.Fatalf("status = %+v", st)
	}
	cancel()
	<-done
}

func TestSupervisorEmptyToken(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{Bin: os.Args[0], Token: " ", MetricsAddr: freeAddr(t)}, nil)
	sup.minBackoff = time.Millisecond
	sup.maxBackoff = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(ctx)
	}()
	waitFor(t, func() bool { return strings.Contains(sup.Status().LastExit, "token is empty") })
	if sup.Status().Running {
		t.Fatal("must not run without a token")
	}
	cancel()
	<-done
}

func TestPublisherStatusIncludesSupervisor(t *testing.T) {
	f := newFakeCF(t)
	p, _ := newTestPublisher(t, f, f.testClientConfig())
	p.supervisor = NewSupervisor(SupervisorConfig{Bin: "cloudflared", Token: "t", MetricsAddr: "127.0.0.1:1"}, nil)
	st := p.Status()
	if st.Supervisor == nil || st.Supervisor.Running {
		t.Fatalf("supervisor status = %+v", st.Supervisor)
	}
}
