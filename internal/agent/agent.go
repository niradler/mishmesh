package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type EndpointSpec struct {
	Name           string
	Kind           string
	Lifecycle      string
	Subdomain      string
	Domain         string
	Port           int
	Private        bool
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
	GatewayURL     string
	Token          string
	GatewayHeaders http.Header
	Log            *slog.Logger
	Endpoints      []EndpointSpec
	Allowlist      []string
	Connects       []ConnectSpec
	Egress         *EgressSpec
	ExitDialer     ExitDialer
	UpstreamRoots  *x509.CertPool
	Clock          func() time.Time
	Out            io.Writer
	UserToken      func(ctx context.Context) (string, error)
}

type Agent struct {
	opts    Options
	log     *slog.Logger
	allow   *Allowlist
	out     io.Writer
	mu      sync.RWMutex
	targets map[string]localTarget
	acked   atomic.Bool
	sess    atomic.Pointer[tunnel.Session]
	dec     *remoteDecider
	origin  *originator
	decrypt *decryptor
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
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	if opts.ExitDialer == nil && opts.Egress != nil && opts.Egress.ServeExit {
		opts.ExitDialer = exitGuard(opts.Egress.ExitAllow, log)
	}
	a := &Agent{opts: opts, log: log, out: out, allow: NewAllowlist(opts.Allowlist), targets: make(map[string]localTarget), dec: newRemoteDecider(clock)}
	a.origin = newOriginator(a, opts.UpstreamRoots)
	if opts.Egress != nil && (opts.Egress.Listen != "" || opts.Egress.SocksListen != "") {
		a.decrypt = newDecryptor(a.dec, log, clock)
	}
	a.dec.onFlush = func() {
		a.origin.closeIdle()
		a.decrypt.flush()
	}
	return a
}

func (a *Agent) Run(parent context.Context) error {
	gatewayURL, err := NormalizeGatewayURL(a.opts.GatewayURL)
	if err != nil {
		return err
	}
	a.opts.GatewayURL = gatewayURL
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	stopConnects, err := a.startConnects(ctx)
	if err != nil {
		return err
	}
	defer stopConnects()

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

	dialOpts := tunnel.DialOptions{Token: a.opts.Token, Header: a.opts.GatewayHeaders}
	if a.opts.UserToken != nil {
		userToken, err := a.opts.UserToken(ctx)
		if err != nil {
			if errors.Is(err, ErrLoginRequired) {
				fail(err)
				return err
			}
			return fmt.Errorf("user credentials: %w", err)
		}
		dialOpts.UserToken = userToken
	}
	dialURL := strings.TrimRight(a.opts.GatewayURL, "/") + tunnel.AgentConnectPath
	conn, err := tunnel.Dial(ctx, dialURL, dialOpts)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	sess, err := tunnel.Client(conn)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("session: %w", err)
	}
	defer sess.Close()
	defer a.sess.CompareAndSwap(sess, nil)
	go func() {
		<-ctx.Done()
		_ = sess.Close()
	}()

	ctrl, err := sess.OpenControl()
	if err != nil {
		return fmt.Errorf("open control: %w", err)
	}
	gen := a.dec.attach(ctrl)
	a.decrypt.attach(gen)
	defer a.decrypt.detach(gen)
	defer a.dec.detach(gen)

	refTarget := make(map[string]localTarget, len(a.opts.Endpoints))
	refSpec := make(map[string]EndpointSpec, len(a.opts.Endpoints))
	var reg tunnel.RegisterPayload
	for idx, sp := range a.opts.Endpoints {
		ref := strconv.Itoa(idx)
		refTarget[ref] = localTarget{addr: sp.LocalTarget, useTLS: sp.TargetTLS, insecure: sp.TargetInsecure}
		refSpec[ref] = sp
		reg.Endpoints = append(reg.Endpoints, tunnel.EndpointRequest{
			Ref:       ref,
			Name:      sp.Name,
			Private:   sp.Private,
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

	a.sess.Store(sess)
	registered := make(chan struct{})
	go a.readControl(ctx, ctrl, gen, refTarget, refSpec, fail, registered)
	go a.pingLoop(ctx, ctrl, cancel)

	a.log.Info("tunnel connected", "gateway", a.opts.GatewayURL)
	for {
		stream, init, err := sess.AcceptData()
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}
		go a.handleStream(stream, init, registered)
	}
}

func (a *Agent) readControl(ctx context.Context, ctrl *tunnel.Control, gen uint64, refTarget map[string]localTarget, refSpec map[string]EndpointSpec, fail context.CancelCauseFunc, registered chan struct{}) {
	var markRegistered sync.Once
	for ctx.Err() == nil {
		msg, err := ctrl.Recv()
		if err != nil {
			return
		}
		switch {
		case msg.Type == tunnel.MsgRegisterAck && msg.RegisterAck != nil:
			a.dec.bind(gen, msg.RegisterAck.OrgID, msg.RegisterAck.AgentID)
			a.decrypt.bind(gen)
			a.handleAck(msg.RegisterAck, refTarget, refSpec, fail)
			markRegistered.Do(func() { close(registered) })
		case msg.Type == tunnel.MsgDecisionResponse && msg.Answer != nil:
			a.dec.deliver(gen, *msg.Answer)
		case msg.Type == tunnel.MsgCertResponse && msg.Cert != nil:
			a.dec.deliverCert(gen, *msg.Cert)
		case msg.Type == tunnel.MsgFlush:
			a.dec.flush()
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

const (
	registrationWait = 10 * time.Second
	pingInterval     = 30 * time.Second
	suspendThreshold = 3 * pingInterval
)

func (a *Agent) pingLoop(ctx context.Context, ctrl *tunnel.Control, reconnect context.CancelFunc) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	last := time.Now().Round(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now().Round(0)
			if gap := now.Sub(last); gap > suspendThreshold {
				a.log.Warn("clock jumped; assuming suspend and reconnecting", "gap", gap)
				reconnect()
				return
			}
			last = now
			if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgPing}); err != nil {
				return
			}
		}
	}
}

func (a *Agent) handleStream(stream net.Conn, init tunnel.StreamInit, registered <-chan struct{}) {
	defer stream.Close()
	if dest, ok := init.Meta[tunnel.MetaEgressExit]; ok && init.EndpointID == "" {
		a.serveExit(stream, dest, init.Meta[tunnel.MetaExitResolve] == "1")
		return
	}
	if init.EndpointID != "" {
		select {
		case <-registered:
		case <-time.After(registrationWait):
		}
	}
	tgt, ok := a.resolveStreamTarget(init)
	if !ok {
		reportStreamFailure(stream, init, notServedReason(init))
		return
	}
	if init.Kind == store.KindUDP {
		a.relayUDP(stream, init, tgt)
		return
	}
	protocol := ""
	if init.Kind == store.KindHTTP {
		protocol = init.Meta["http_protocol"]
	}
	local, err := dialTarget(tgt, protocol)
	if err != nil {
		a.log.Warn("dial local target failed", "target", tgt.addr, "err", err)
		reportStreamFailure(stream, init, dialFailureReason(err))
		return
	}
	defer local.Close()

	tunnel.Splice(stream, local)
}

func dialTarget(tgt localTarget, protocol string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", tgt.addr, 30*time.Second)
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
	config := &tls.Config{ServerName: host, InsecureSkipVerify: tgt.insecure}
	if protocol == "h2" {
		config.NextProtos = []string{"h2"}
	}
	tc := tls.Client(conn, config)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("local tls handshake: %w", err)
	}
	if protocol == "h2" && tc.ConnectionState().NegotiatedProtocol != "h2" {
		_ = tc.Close()
		return nil, errors.New("local tls handshake: upstream does not support HTTP/2")
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
