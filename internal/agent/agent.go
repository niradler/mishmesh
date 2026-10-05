package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type EndpointSpec struct {
	Name           string
	Kind           string
	Lifecycle      string
	Subdomain      string
	Domain         string
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
	Out        io.Writer
}

type Agent struct {
	opts    Options
	log     *slog.Logger
	allow   *Allowlist
	out     io.Writer
	mu      sync.RWMutex
	targets map[string]localTarget
	acked   atomic.Bool
}

func New(opts Options) *Agent {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	return &Agent{opts: opts, log: log, out: out, allow: NewAllowlist(opts.Allowlist), targets: make(map[string]localTarget)}
}

func (a *Agent) Run(parent context.Context) error {
	gatewayURL, err := NormalizeGatewayURL(a.opts.GatewayURL)
	if err != nil {
		return err
	}
	a.opts.GatewayURL = gatewayURL
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	backoff := initialBackoff
	for {
		started := time.Now()
		err := a.connectOnce(ctx, cancel)
		if fatal := fatalCause(ctx); fatal != nil {
			return fatal
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if fatal := classifyDialError(err); fatal != nil {
			return fatal
		}
		if time.Since(started) >= stableSessionAfter {
			backoff = initialBackoff
		}
		delay := jittered(backoff)
		a.log.Warn("tunnel session ended; reconnecting", "err", err, "retry_in", delay)
		select {
		case <-ctx.Done():
			if fatal := fatalCause(ctx); fatal != nil {
				return fatal
			}
			return ctx.Err()
		case <-time.After(delay):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

const (
	initialBackoff     = time.Second
	maxBackoff         = 30 * time.Second
	stableSessionAfter = 30 * time.Second
)

func jittered(d time.Duration) time.Duration {
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func (a *Agent) connectOnce(parent context.Context, fail context.CancelCauseFunc) error {
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
	go func() {
		<-ctx.Done()
		_ = sess.Close()
	}()

	ctrl, err := sess.OpenControl()
	if err != nil {
		return fmt.Errorf("open control: %w", err)
	}

	refTarget := make(map[string]localTarget, len(a.opts.Endpoints))
	refSpec := make(map[string]EndpointSpec, len(a.opts.Endpoints))
	var reg tunnel.RegisterPayload
	for idx, sp := range a.opts.Endpoints {
		ref := strconv.Itoa(idx)
		refTarget[ref] = localTarget{addr: sp.LocalTarget, useTLS: sp.TargetTLS, insecure: sp.TargetInsecure}
		refSpec[ref] = sp
		reg.Endpoints = append(reg.Endpoints, tunnel.EndpointRequest{
			Ref:       ref,
			Kind:      sp.Kind,
			Lifecycle: sp.Lifecycle,
			Subdomain: sp.Subdomain,
			Domain:    sp.Domain,
			Port:      sp.Port,
			Policy:    sp.Policy,
		})
	}
	if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgRegister, Register: &reg}); err != nil {
		return fmt.Errorf("send register: %w", err)
	}

	go a.readControl(ctx, ctrl, refTarget, refSpec, fail)
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

func (a *Agent) readControl(ctx context.Context, ctrl *tunnel.Control, refTarget map[string]localTarget, refSpec map[string]EndpointSpec, fail context.CancelCauseFunc) {
	for ctx.Err() == nil {
		msg, err := ctrl.Recv()
		if err != nil {
			return
		}
		if msg.Type == tunnel.MsgRegisterAck && msg.RegisterAck != nil {
			a.handleAck(msg.RegisterAck, refTarget, refSpec, fail)
		}
	}
}

func (a *Agent) handleAck(ack *tunnel.RegisterAckPayload, refTarget map[string]localTarget, refSpec map[string]EndpointSpec, fail context.CancelCauseFunc) {
	results := make([]TunnelResult, 0, len(ack.Endpoints))
	failed := 0
	for _, b := range ack.Endpoints {
		tgt := refTarget[b.Ref]
		spec := refSpec[b.Ref]
		results = append(results, TunnelResult{Name: spec.Name, Kind: spec.Kind, LocalTarget: tgt.addr, Binding: b})
		if b.EndpointID == "" {
			failed++
			a.log.Warn("endpoint registration failed", "tunnel", spec.Name, "ref", b.Ref, "reason", b.Error)
			continue
		}
		a.setTarget(b.EndpointID, tgt)
	}
	fmt.Fprint(a.out, FormatResults(a.opts.GatewayURL, results))
	if failed > 0 && !a.acked.Swap(true) {
		fail(&RegistrationError{Failed: failed, Total: len(ack.Endpoints), Results: results})
		return
	}
	a.acked.Store(true)
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

	tunnel.Splice(stream, local)
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
