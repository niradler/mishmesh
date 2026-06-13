package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
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
	Kind           string
	Lifecycle      string
	Subdomain      string
	Port           int
	LocalTarget    string
	TargetTLS      bool
	TargetInsecure bool
	Policy         json.RawMessage
}

type localTarget struct {
	addr       string
	useTLS     bool
	insecure   bool
	serverName string
}

type Options struct {
	GatewayURL string
	Token      string
	Log        *slog.Logger
	Endpoints  []EndpointSpec
	Allowlist  []string
}

type Agent struct {
	opts    Options
	log     *slog.Logger
	allow   *Allowlist
	mu      sync.RWMutex
	targets map[string]localTarget
}

func New(opts Options) *Agent {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Agent{opts: opts, log: log, allow: NewAllowlist(opts.Allowlist), targets: make(map[string]localTarget)}
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

	refTarget := make(map[string]localTarget, len(a.opts.Endpoints))
	var reg tunnel.RegisterPayload
	for idx, sp := range a.opts.Endpoints {
		ref := strconv.Itoa(idx)
		refTarget[ref] = localTarget{addr: sp.LocalTarget, useTLS: sp.TargetTLS, insecure: sp.TargetInsecure}
		reg.Endpoints = append(reg.Endpoints, tunnel.EndpointRequest{
			Ref:       ref,
			Kind:      sp.Kind,
			Lifecycle: sp.Lifecycle,
			Subdomain: sp.Subdomain,
			Port:      sp.Port,
			Policy:    sp.Policy,
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

func (a *Agent) readControl(ctx context.Context, ctrl *tunnel.Control, refTarget map[string]localTarget) {
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
				tgt := refTarget[b.Ref]
				a.setTarget(b.EndpointID, tgt)
				fmt.Printf("  %s  ->  %s\n", b.PublicURL, tgt.addr)
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
	tgt, ok := a.resolveStreamTarget(init)
	if !ok {
		return
	}
	local, err := dialTarget(tgt)
	if err != nil {
		a.log.Warn("dial local target failed", "target", tgt.addr, "err", err)
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, stream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stream, local); done <- struct{}{} }()
	<-done
}

func dialTarget(tgt localTarget) (net.Conn, error) {
	conn, err := net.Dial("tcp", tgt.addr)
	if err != nil {
		return nil, err
	}
	if !tgt.useTLS {
		return conn, nil
	}
	host := tgt.serverName
	if host == "" {
		if h, _, splitErr := net.SplitHostPort(tgt.addr); splitErr == nil {
			host = h
		} else {
			host = tgt.addr
		}
	}
	tc := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: tgt.insecure})
	if err := tc.Handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("local tls handshake: %w", err)
	}
	return tc, nil
}

func (a *Agent) resolveStreamTarget(init tunnel.StreamInit) (localTarget, bool) {
	if init.EndpointID == "" {
		target := init.Meta["target"]
		if target == "" {
			a.log.Warn("reach-in stream without target")
			return localTarget{}, false
		}
		dialAddr, serverName, ok := a.allow.Resolve(target)
		if !ok {
			a.log.Warn("reach-in target denied by allowlist", "target", target)
			return localTarget{}, false
		}
		return localTarget{addr: dialAddr, serverName: serverName, useTLS: init.Meta["tls"] == "true", insecure: init.Meta["insecure"] == "true"}, true
	}
	tgt, ok := a.targetFor(init.EndpointID)
	if !ok || tgt.addr == "" {
		a.log.Warn("stream for unknown endpoint", "endpoint_id", init.EndpointID)
		return localTarget{}, false
	}
	return tgt, true
}

func (a *Agent) setTarget(endpointID string, target localTarget) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.targets[endpointID] = target
}

func (a *Agent) targetFor(endpointID string) (localTarget, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	t, ok := a.targets[endpointID]
	return t, ok
}
