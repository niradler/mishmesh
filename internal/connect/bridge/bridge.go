package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type Meter interface {
	AddBytes(kind string, in, out int64)
}

type Revocations interface {
	Subscribe(fn func(identity.Revocation)) func()
}

type Options struct {
	Data        store.DataStore
	Conns       store.ConnectionStore
	Log         *slog.Logger
	Meter       Meter
	Deciders    identity.Deciders
	Revocations Revocations
}

type flow struct {
	orgID     string
	sessionID string
	conns     []net.Conn
	revoked   bool
}

type Bridge struct {
	data     store.DataStore
	conns    store.ConnectionStore
	log      *slog.Logger
	meter    Meter
	deciders identity.Deciders

	mu    sync.Mutex
	flows map[*flow]struct{}
}

const (
	lookupTimeout   = 5 * time.Second
	decisionTimeout = 5 * time.Second
	agentClientIP   = "0.0.0.0"
)

var (
	ErrKindMismatch   = errors.New("connect: endpoint kind does not match the stream")
	ErrNotAllowed     = errors.New("connect: agent is not allowed to connect to this endpoint")
	ErrOriginOffline  = errors.New("connect: endpoint has no connected origin")
	ErrMissingConnect = errors.New("connect: stream does not name an endpoint")
	ErrUnavailable    = errors.New("connect: authorization unavailable")
)

func New(opts Options) *Bridge {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	b := &Bridge{data: opts.Data, conns: opts.Conns, log: log, meter: opts.Meter, deciders: opts.Deciders, flows: make(map[*flow]struct{})}
	if opts.Revocations != nil {
		opts.Revocations.Subscribe(b.revoke)
	}
	return b
}

func (b *Bridge) HandleConnect(ctx context.Context, agent *store.Agent, stream net.Conn, init tunnel.StreamInit) {
	defer stream.Close()
	var f *flow
	if p, ok := identity.PrincipalFrom(ctx); ok {
		f = &flow{orgID: agent.OrgID, sessionID: p.SessionID, conns: []net.Conn{stream}}
		defer b.trackFlow(f)()
	}
	ep, origin, err := b.authorize(ctx, agent, init)
	if err != nil {
		b.log.Warn("connect refused", "agent_id", agent.ID, "connect", init.Meta[tunnel.MetaConnect], "kind", init.Kind, "err", err)
		return
	}
	meta := map[string]string{}
	if v := init.Meta[tunnel.MetaIdleTimeout]; v != "" {
		meta[tunnel.MetaIdleTimeout] = v
	}
	upstream, err := origin.OpenStream(ctx, ep.ID, ep.Kind, meta)
	if err != nil {
		b.log.Warn("connect origin stream failed", "agent_id", agent.ID, "endpoint_id", ep.ID, "err", err)
		return
	}
	defer upstream.Close()
	if f != nil && !b.attach(f, upstream) {
		b.log.Info("connect flow revoked before splice", "agent_id", agent.ID, "endpoint_id", ep.ID)
		return
	}
	b.log.Debug("connect bridged", "agent_id", agent.ID, "endpoint_id", ep.ID, "kind", ep.Kind)
	up, down := tunnel.Splice(stream, upstream)
	b.conns.AddUsage(ep.OrgID, up+down)
	if b.meter != nil {
		b.meter.AddBytes(ep.Kind, up, down)
	}
}

func (b *Bridge) DecideConnect(ctx context.Context, agent *store.Agent, q tunnel.DecisionQuery) tunnel.DecisionAnswer {
	if q.Action != tunnel.DecisionActionConnect {
		return tunnel.DecisionAnswer{Reason: string(authz.ReasonInvalidRequest)}
	}
	decision, err := b.decide(ctx, agent, q.Connect, q.Kind)
	if err != nil {
		reason := string(authz.ReasonPolicyDeny)
		if errors.Is(err, ErrUnavailable) {
			reason = string(authz.ReasonUnavailable)
		}
		if decision.Reason != "" {
			reason = string(decision.Reason)
		}
		return tunnel.DecisionAnswer{Reason: reason}
	}
	return tunnel.DecisionAnswer{Allowed: true, Reason: string(decision.Reason), TTLSeconds: decision.TTLSeconds}
}

func (b *Bridge) authorize(ctx context.Context, agent *store.Agent, init tunnel.StreamInit) (*store.Endpoint, store.AgentConn, error) {
	d, err := b.decide(ctx, agent, init.Meta[tunnel.MetaConnect], init.Kind)
	if err != nil {
		return nil, nil, err
	}
	origin, ok := b.conns.ResolveEndpoint(d.endpoint.ID)
	if !ok {
		return nil, nil, ErrOriginOffline
	}
	return d.endpoint, origin, nil
}

type connectDecision struct {
	authz.Decision
	endpoint *store.Endpoint
}

func (b *Bridge) decide(ctx context.Context, agent *store.Agent, ref, kind string) (connectDecision, error) {
	if ref == "" {
		return connectDecision{}, ErrMissingConnect
	}
	if b.deciders == nil {
		ep, err := b.legacyAuthorize(ctx, agent, ref, kind)
		if err != nil {
			return connectDecision{}, err
		}
		return connectDecision{Decision: authz.Decision{Allowed: true, Reason: authz.ReasonAllow}, endpoint: ep}, nil
	}
	ep, err := b.lookup(ctx, agent, ref, kind)
	if err != nil {
		return connectDecision{}, err
	}
	scope := identity.Scope{OrgID: agent.OrgID, AgentID: agent.ID}
	principal := authz.EntityRef{Type: authz.EntityAgent, ID: agent.ID}
	reqCtx := authz.RequestContext{ClientIP: agentClientIP, Protocol: kind, AuthMethod: authz.AuthMethodAgentToken}
	if p, ok := identity.PrincipalFrom(ctx); ok {
		principal = p.Ref
		scope.TokenSessionID = p.SessionID
		reqCtx.AuthMethod = p.AuthMethod
		authTime := p.AuthTime.UTC()
		reqCtx.AuthTime = &authTime
	}
	decideCtx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	d, err := b.deciders.Decider(scope).Decide(decideCtx, authz.DecisionRequest{
		Principal: principal,
		Action:    authz.ActionAgentConnect,
		Resource:  authz.EntityRef{Type: authz.EntityEndpoint, ID: ep.ID},
		Context:   reqCtx,
	})
	out := connectDecision{Decision: d, endpoint: ep}
	if err != nil || d.Reason == authz.ReasonUnavailable {
		return out, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !d.Allowed {
		return out, fmt.Errorf("%w: %s", ErrNotAllowed, d.Reason)
	}
	return out, nil
}

func (b *Bridge) lookup(ctx context.Context, agent *store.Agent, ref, kind string) (*store.Endpoint, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	ep, err := store.FindConnectEndpoint(lookupCtx, b.data, agent.OrgID, ref)
	if err != nil {
		return nil, fmt.Errorf("connect: endpoint %q: %w", ref, err)
	}
	if ep.Kind != kind {
		return nil, fmt.Errorf("%w: endpoint is %s, stream is %s", ErrKindMismatch, ep.Kind, kind)
	}
	return ep, nil
}

func (b *Bridge) legacyAuthorize(ctx context.Context, agent *store.Agent, ref, kind string) (*store.Endpoint, error) {
	ep, err := b.lookup(ctx, agent, ref, kind)
	if err != nil {
		return nil, err
	}
	if ep.Policy == nil || !ep.Policy.Connect.Allows(agent) {
		return nil, ErrNotAllowed
	}
	return ep, nil
}

func (b *Bridge) trackFlow(f *flow) func() {
	b.mu.Lock()
	b.flows[f] = struct{}{}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.flows, f)
		b.mu.Unlock()
	}
}

func (b *Bridge) attach(f *flow, c net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f.revoked {
		return false
	}
	f.conns = append(f.conns, c)
	return true
}

func (b *Bridge) revoke(rev identity.Revocation) {
	if len(rev.SessionIDs) == 0 {
		return
	}
	var doomed []net.Conn
	hits := 0
	b.mu.Lock()
	for f := range b.flows {
		if f.orgID == rev.OrgID && slices.Contains(rev.SessionIDs, f.sessionID) {
			f.revoked = true
			doomed = append(doomed, f.conns...)
			hits++
		}
	}
	b.mu.Unlock()
	for _, c := range doomed {
		_ = c.Close()
	}
	if hits > 0 {
		b.log.Info("closed connect flows for revoked sessions", "org_id", rev.OrgID, "flows", hits)
	}
}
