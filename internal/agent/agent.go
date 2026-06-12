package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type EndpointSpec struct {
	Kind        string
	Lifecycle   string
	Subdomain   string
	Port        int
	LocalTarget string
}

type Options struct {
	GatewayURL string
	Token      string
	Log        *slog.Logger
	Endpoints  []EndpointSpec
}

type Agent struct {
	opts    Options
	log     *slog.Logger
	mu      sync.RWMutex
	targets map[string]string
}

func New(opts Options) *Agent {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Agent{opts: opts, log: log, targets: make(map[string]string)}
}

func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		err := a.connectOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.log.Warn("tunnel session ended; reconnecting", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (a *Agent) connectOnce(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	dialURL := strings.TrimRight(a.opts.GatewayURL, "/") + tunnel.AgentConnectPath
	conn, err := tunnel.Dial(ctx, dialURL, tunnel.DialOptions{Token: a.opts.Token})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	sess, err := tunnel.Client(conn)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("session: %w", err)
	}
	defer sess.Close()

	ctrl, err := sess.OpenControl()
	if err != nil {
		return fmt.Errorf("open control: %w", err)
	}

	refTarget := make(map[string]string, len(a.opts.Endpoints))
	var reg tunnel.RegisterPayload
	for idx, sp := range a.opts.Endpoints {
		ref := strconv.Itoa(idx)
		refTarget[ref] = sp.LocalTarget
		reg.Endpoints = append(reg.Endpoints, tunnel.EndpointRequest{
			Ref:       ref,
			Kind:      sp.Kind,
			Lifecycle: sp.Lifecycle,
			Subdomain: sp.Subdomain,
			Port:      sp.Port,
		})
	}
	if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgRegister, Register: &reg}); err != nil {
		return fmt.Errorf("send register: %w", err)
	}

	go a.readControl(ctx, ctrl, refTarget)
	go a.pingLoop(ctx, ctrl)

	a.log.Info("tunnel connected", "gateway", a.opts.GatewayURL)
	for {
		stream, init, err := sess.AcceptData()
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}
		go a.handleStream(stream, init)
	}
}

func (a *Agent) readControl(ctx context.Context, ctrl *tunnel.Control, refTarget map[string]string) {
	for ctx.Err() == nil {
		msg, err := ctrl.Recv()
		if err != nil {
			return
		}
		if msg.Type == tunnel.MsgRegisterAck && msg.RegisterAck != nil {
			for _, b := range msg.RegisterAck.Endpoints {
				if b.EndpointID == "" {
					a.log.Warn("endpoint registration failed", "ref", b.Ref)
					continue
				}
				a.setTarget(b.EndpointID, refTarget[b.Ref])
				fmt.Printf("  %s  ->  %s\n", b.PublicURL, refTarget[b.Ref])
			}
		}
	}
}

func (a *Agent) pingLoop(ctx context.Context, ctrl *tunnel.Control) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgPing}); err != nil {
				return
			}
		}
	}
}

func (a *Agent) handleStream(stream net.Conn, init tunnel.StreamInit) {
	defer stream.Close()
	target := a.targetFor(init.EndpointID)
	if target == "" {
		a.log.Warn("stream for unknown endpoint", "endpoint_id", init.EndpointID)
		return
	}
	local, err := net.Dial("tcp", target)
	if err != nil {
		a.log.Warn("dial local target failed", "target", target, "err", err)
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, stream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stream, local); done <- struct{}{} }()
	<-done
}

func (a *Agent) setTarget(endpointID, target string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.targets[endpointID] = target
}

func (a *Agent) targetFor(endpointID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.targets[endpointID]
}
