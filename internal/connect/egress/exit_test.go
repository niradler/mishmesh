package egress

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type scriptedExit struct {
	id     string
	answer func(io.Writer) error
	meta   map[string]string
}

func (e *scriptedExit) AgentID() string { return e.id }
func (e *scriptedExit) Close() error    { return nil }

func (e *scriptedExit) OpenStream(_ context.Context, _, _ string, meta map[string]string) (net.Conn, error) {
	e.meta = meta
	near, far := net.Pipe()
	go func() {
		defer far.Close()
		_ = e.answer(far)
		_, _ = io.Copy(io.Discard, far)
	}()
	return near, nil
}

func TestOpenExitReadsResolvedTargets(t *testing.T) {
	cases := []struct {
		name    string
		answer  func(io.Writer) error
		targets []string
		reason  string
	}{
		{"resolved answers", func(w io.Writer) error {
			return tunnel.WriteExitTargets(w, []string{"203.0.113.7:443", "[2001:db8::1]:443"})
		}, []string{"203.0.113.7:443", "[2001:db8::1]:443"}, ""},
		{"outdated exit dials without reporting", func(w io.Writer) error {
			return tunnel.WriteEgressReply(w, tunnel.EgressReply{Status: tunnel.EgressOK})
		}, nil, reasonExitOutdated},
		{"exit refusal", func(w io.Writer) error {
			return tunnel.WriteEgressReply(w, tunnel.EgressReply{Status: tunnel.EgressDenied, Reason: "blocked_destination"})
		}, nil, "exit_blocked_destination"},
		{"names instead of ips", func(w io.Writer) error {
			return tunnel.WriteExitTargets(w, []string{"svc.example.com:443"})
		}, nil, reasonExitProtocol},
		{"empty answer", func(w io.Writer) error {
			return tunnel.WriteExitTargets(w, nil)
		}, nil, reasonExitProtocol},
		{"garbage", func(w io.Writer) error {
			_, err := io.WriteString(w, "HTTP/1.1 200 OK\r\n\r\n")
			return err
		}, nil, reasonExitProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conns := memory.NewConnStore()
			exit := &scriptedExit{id: "ag_exit", answer: tc.answer}
			conns.AddAgent(exit)
			h := New(Options{Conns: conns, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, targets, refused := h.openExit(ctx, &store.Agent{ID: "ag_exit"}, "svc.example.com:443")
			if exit.meta[tunnel.MetaExitResolve] != "1" || exit.meta[tunnel.MetaEgressExit] != "svc.example.com:443" {
				t.Fatalf("exit stream meta = %v", exit.meta)
			}
			if tc.reason != "" {
				if refused == nil || refused.reason != tc.reason || stream != nil {
					t.Fatalf("refusal = %+v stream %v, want %s", refused, stream, tc.reason)
				}
				return
			}
			if refused != nil {
				t.Fatalf("refused: %+v", refused)
			}
			defer stream.Close()
			if len(targets) != len(tc.targets) || targets[0] != tc.targets[0] || targets[1] != tc.targets[1] {
				t.Fatalf("targets = %v", targets)
			}
		})
	}
}

type recordedEvents struct{ events []store.AccessEvent }

func (r *recordedEvents) Record(e *store.AccessEvent) { r.events = append(r.events, *e) }

func TestEgressRefusesNonTCPStreams(t *testing.T) {
	for _, kind := range []string{store.KindUDP, "", "quic"} {
		access := &recordedEvents{}
		h := New(Options{Conns: memory.NewConnStore(), Access: access, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		agentSide, serverSide := net.Pipe()
		go h.HandleEgress(context.Background(), &store.Agent{ID: "ag_1", OrgID: "org_1"}, serverSide, tunnel.StreamInit{Kind: kind, Meta: map[string]string{tunnel.MetaEgress: "example.com:443"}})
		_ = agentSide.SetDeadline(time.Now().Add(5 * time.Second))
		reply, err := tunnel.ReadEgressReply(agentSide)
		if err != nil || reply.Status != tunnel.EgressInvalid || reply.Reason != reasonNotTCP {
			t.Fatalf("kind %q: reply %+v err %v", kind, reply, err)
		}
		_, _ = io.ReadAll(agentSide)
		if len(access.events) != 1 || access.events[0].Reason != reasonNotTCP || access.events[0].Decision != store.AccessDeny {
			t.Fatalf("kind %q: access events %+v", kind, access.events)
		}
	}
}
