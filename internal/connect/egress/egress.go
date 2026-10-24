package egress

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/flows"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	MeterKind         = "egress"
	decisionTimeout   = 5 * time.Second
	dialTimeout       = 10 * time.Second
	replyTimeout      = 5 * time.Second
	agentClientIP     = "0.0.0.0"
	reasonBlocked     = "blocked_destination"
	reasonDial        = "dial_failed"
	reasonResolve     = "resolve_failed"
	reasonSNI         = "sni_mismatch"
	reasonBadCreds    = "invalid_proxy_credentials"
	reasonNoAuth      = "proxy_auth_unavailable"
	reasonUnknownExit = "unknown_exit"
	reasonExitOffline = "exit_offline"
	reasonExitSelf    = "exit_is_self"
	reasonNoExits     = "exits_unavailable"
)

type Dialer interface {
	ResolveAll(ctx context.Context, target string) ([]string, error)
	Dial(ctx context.Context, target string, otls *store.OriginTLS, timeout time.Duration) (net.Conn, error)
}

type Meter interface {
	AddBytes(kind string, in, out int64)
}

type Recorder interface {
	Record(e *store.AccessEvent)
}

type Authenticator interface {
	Authenticate(ctx context.Context, raw string, allowed ...token.Type) (identity.Principal, error)
}

type Agents interface {
	ListAgents(ctx context.Context, orgID string) ([]*store.Agent, error)
}

type Options struct {
	Conns       store.ConnectionStore
	Log         *slog.Logger
	Meter       Meter
	Deciders    identity.Deciders
	Dialer      Dialer
	Access      Recorder
	Revocations flows.Revocations
	Auth        Authenticator
	Agents      Agents
}

type Handler struct {
	conns    store.ConnectionStore
	log      *slog.Logger
	meter    Meter
	deciders identity.Deciders
	dialer   Dialer
	access   Recorder
	auth     Authenticator
	agents   Agents
	flows    *flows.Tracker
}

func New(opts Options) *Handler {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		conns:    opts.Conns,
		log:      log,
		meter:    opts.Meter,
		deciders: opts.Deciders,
		dialer:   opts.Dialer,
		access:   opts.Access,
		auth:     opts.Auth,
		agents:   opts.Agents,
		flows:    flows.NewTracker(log, "egress", opts.Revocations),
	}
}

type flowRecord struct {
	event   store.AccessEvent
	started time.Time
}

type refusal struct {
	status tunnel.EgressStatus
	reason string
}

func (h *Handler) HandleEgress(ctx context.Context, agent *store.Agent, stream net.Conn, init tunnel.StreamInit) {
	defer stream.Close()
	rec := &flowRecord{started: time.Now(), event: store.AccessEvent{
		OrgID:         agent.OrgID,
		Source:        store.AccessSourceEgress,
		AgentID:       agent.ID,
		PrincipalType: string(authz.EntityAgent),
		PrincipalID:   agent.ID,
		ClientIP:      egressClientIP(init.Meta[tunnel.MetaEgressClient]),
		Protocol:      "tcp",
	}}
	dest, err := authz.NormalizeDestination(init.Meta[tunnel.MetaEgress])
	if err != nil {
		h.refuse(stream, rec, &refusal{tunnel.EgressInvalid, string(authz.ReasonInvalidRequest)})
		return
	}
	rec.event.Destination = dest
	p, hasPrincipal, refused := h.principal(ctx, agent, init.Meta[tunnel.MetaEgressAuth])
	if refused != nil {
		h.refuse(stream, rec, refused)
		return
	}
	if hasPrincipal {
		rec.event.PrincipalType = string(p.Ref.Type)
		rec.event.PrincipalID = p.Ref.ID
		rec.event.SessionID = p.SessionID
	}
	exit, refused := h.resolveExit(ctx, agent, init.Meta[tunnel.MetaEgressVia])
	if refused != nil {
		h.refuse(stream, rec, refused)
		return
	}
	targets := []string{dest}
	if exit != nil {
		rec.event.ExitAgentID = exit.ID
	} else {
		if targets, refused = h.resolve(ctx, agent, dest); refused != nil {
			h.refuse(stream, rec, refused)
			return
		}
	}
	var flow *flows.Flow
	if hasPrincipal {
		var untrack func()
		flow, untrack = h.flows.Track(agent.OrgID, p.SessionID, stream)
		defer untrack()
	}
	decision, status := h.decideAll(ctx, agent, p, hasPrincipal, rec.event.ClientIP, dest, targets, exit)
	rec.event.PolicyIDs = policyIDs(decision)
	if status != tunnel.EgressOK {
		h.refuse(stream, rec, &refusal{status, string(decision.Reason)})
		return
	}
	upstream, refused := h.dial(ctx, agent, exit, dest, targets)
	if refused != nil {
		h.refuse(stream, rec, refused)
		return
	}
	defer upstream.Close()
	if flow != nil && !h.flows.Attach(flow, upstream) {
		h.refuse(stream, rec, &refusal{tunnel.EgressDenied, string(authz.ReasonSessionRevoked)})
		return
	}
	if !h.reply(stream, tunnel.EgressReply{Status: tunnel.EgressOK}) {
		return
	}
	host, _, _ := net.SplitHostPort(dest)
	client := newSNIConn(stream, host)
	up, down := tunnel.Splice(client, upstream)
	h.conns.AddUsage(agent.OrgID, up+down)
	if h.meter != nil {
		h.meter.AddBytes(MeterKind, up, down)
	}
	rec.event.Decision = store.AccessAllow
	rec.event.Reason = string(authz.ReasonAllow)
	if client.mismatch() {
		rec.event.Decision = store.AccessDeny
		rec.event.Reason = reasonSNI
		h.log.Warn("egress tls sni does not match destination", "agent_id", agent.ID, "destination", dest)
	}
	rec.event.BytesIn = up
	rec.event.BytesOut = down
	h.record(rec)
}

func (h *Handler) principal(ctx context.Context, agent *store.Agent, raw string) (identity.Principal, bool, *refusal) {
	if raw == "" {
		p, ok := identity.PrincipalFrom(ctx)
		return p, ok, nil
	}
	if h.auth == nil {
		return identity.Principal{}, false, &refusal{tunnel.EgressUnavailable, reasonNoAuth}
	}
	p, err := h.auth.Authenticate(ctx, raw, token.TypeAccess, token.TypeAgentUser)
	switch {
	case errors.Is(err, identity.ErrUnavailable):
		return identity.Principal{}, false, &refusal{tunnel.EgressUnavailable, string(authz.ReasonUnavailable)}
	case err != nil || p.OrgID != agent.OrgID:
		return identity.Principal{}, false, &refusal{tunnel.EgressAuthRequired, reasonBadCreds}
	}
	return p, true, nil
}

func (h *Handler) resolveExit(ctx context.Context, agent *store.Agent, ref string) (*store.Agent, *refusal) {
	if ref == "" {
		return nil, nil
	}
	if h.agents == nil {
		return nil, &refusal{tunnel.EgressUnavailable, reasonNoExits}
	}
	agents, err := h.agents.ListAgents(ctx, agent.OrgID)
	if err != nil {
		h.log.Error("egress exit lookup failed", "org_id", agent.OrgID, "err", err)
		return nil, &refusal{tunnel.EgressUnavailable, string(authz.ReasonUnavailable)}
	}
	var match *store.Agent
	for _, a := range agents {
		if a.OrgID != agent.OrgID || (a.ID != ref && a.Name != ref) {
			continue
		}
		if match != nil {
			return nil, &refusal{tunnel.EgressDenied, reasonUnknownExit}
		}
		match = a
	}
	switch {
	case match == nil:
		return nil, &refusal{tunnel.EgressDenied, reasonUnknownExit}
	case match.ID == agent.ID:
		return nil, &refusal{tunnel.EgressInvalid, reasonExitSelf}
	}
	return match, nil
}

func (h *Handler) resolve(ctx context.Context, agent *store.Agent, dest string) ([]string, *refusal) {
	if h.dialer == nil {
		return nil, &refusal{tunnel.EgressUnavailable, string(authz.ReasonUnavailable)}
	}
	resolveCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	targets, err := h.dialer.ResolveAll(resolveCtx, dest)
	if err != nil {
		h.log.Info("egress destination refused", "agent_id", agent.ID, "destination", dest, "err", err)
		if errors.Is(err, proxy.ErrBlocked) {
			return nil, &refusal{tunnel.EgressDenied, reasonBlocked}
		}
		return nil, &refusal{tunnel.EgressDialFailed, reasonResolve}
	}
	return targets, nil
}

func (h *Handler) dial(ctx context.Context, agent, exit *store.Agent, dest string, targets []string) (net.Conn, *refusal) {
	if exit != nil {
		return h.dialViaExit(ctx, exit, dest)
	}
	var upstream net.Conn
	var err error
	for _, target := range targets {
		if upstream, err = h.dialer.Dial(ctx, target, nil, dialTimeout); err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		h.log.Info("egress dial refused", "agent_id", agent.ID, "destination", dest, "err", err)
		if errors.Is(err, proxy.ErrBlocked) {
			return nil, &refusal{tunnel.EgressDenied, reasonBlocked}
		}
		return nil, &refusal{tunnel.EgressDialFailed, reasonDial}
	}
	return upstream, nil
}

func (h *Handler) dialViaExit(ctx context.Context, exit *store.Agent, dest string) (net.Conn, *refusal) {
	conn, ok := h.conns.GetAgent(exit.ID)
	if !ok {
		return nil, &refusal{tunnel.EgressUnavailable, reasonExitOffline}
	}
	openCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	stream, err := conn.OpenStream(openCtx, "", store.KindTCP, map[string]string{tunnel.MetaEgressExit: dest})
	if err != nil {
		h.log.Info("egress exit stream failed", "exit_id", exit.ID, "err", err)
		return nil, &refusal{tunnel.EgressUnavailable, reasonExitOffline}
	}
	_ = stream.SetReadDeadline(time.Now().Add(tunnel.EgressReplyTimeout))
	reply, err := tunnel.ReadEgressReply(stream)
	_ = stream.SetReadDeadline(time.Time{})
	if err != nil {
		_ = stream.Close()
		return nil, &refusal{tunnel.EgressDialFailed, reasonDial}
	}
	if reply.Status != tunnel.EgressOK {
		_ = stream.Close()
		return nil, &refusal{reply.Status, "exit_" + reply.Reason}
	}
	return stream, nil
}

func (h *Handler) decideAll(ctx context.Context, agent *store.Agent, p identity.Principal, hasPrincipal bool, clientIP, dest string, targets []string, exit *store.Agent) (authz.Decision, tunnel.EgressStatus) {
	var first authz.Decision
	for i, target := range targets {
		d, status := h.decide(ctx, agent, p, hasPrincipal, clientIP, dest, target, exit)
		if status != tunnel.EgressOK {
			return d, status
		}
		if i == 0 {
			first = d
		}
	}
	return first, tunnel.EgressOK
}

func (h *Handler) decide(ctx context.Context, agent *store.Agent, p identity.Principal, hasPrincipal bool, clientIP, dest, target string, exit *store.Agent) (authz.Decision, tunnel.EgressStatus) {
	if h.deciders == nil || h.dialer == nil {
		return authz.Decision{Reason: authz.ReasonUnavailable}, tunnel.EgressUnavailable
	}
	scope := identity.Scope{OrgID: agent.OrgID, AgentID: agent.ID}
	principal := authz.EntityRef{Type: authz.EntityAgent, ID: agent.ID}
	reqCtx := authz.RequestContext{ClientIP: clientIP, AuthMethod: authz.AuthMethodAgentToken}
	if exit != nil {
		reqCtx.Exit = exit.ID
	} else if host, _, err := net.SplitHostPort(target); err == nil {
		reqCtx.ResolvedIP = host
	}
	if hasPrincipal {
		principal = p.Ref
		scope.TokenSessionID = p.SessionID
		reqCtx.AuthMethod = p.AuthMethod
		authTime := p.AuthTime.UTC()
		reqCtx.AuthTime = &authTime
	}
	decideCtx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	d, err := h.deciders.Decider(scope).Decide(decideCtx, authz.DecisionRequest{
		Principal: principal,
		Action:    authz.ActionEgressConnect,
		Resource:  authz.EntityRef{Type: authz.EntityDestination, ID: dest},
		Context:   reqCtx,
	})
	switch {
	case err != nil || d.Reason == authz.ReasonUnavailable:
		d.Allowed = false
		d.Reason = authz.ReasonUnavailable
		return d, tunnel.EgressUnavailable
	case !d.Allowed:
		if d.Reason == "" {
			d.Reason = authz.ReasonPolicyDeny
		}
		return d, tunnel.EgressDenied
	}
	return d, tunnel.EgressOK
}

func (h *Handler) refuse(stream net.Conn, rec *flowRecord, r *refusal) {
	h.reply(stream, tunnel.EgressReply{Status: r.status, Reason: r.reason})
	rec.event.Decision = store.AccessDeny
	rec.event.Reason = truncate(r.reason)
	h.record(rec)
}

func (h *Handler) reply(stream net.Conn, reply tunnel.EgressReply) bool {
	_ = stream.SetWriteDeadline(time.Now().Add(replyTimeout))
	err := tunnel.WriteEgressReply(stream, reply)
	_ = stream.SetWriteDeadline(time.Time{})
	return err == nil
}

func (h *Handler) record(rec *flowRecord) {
	if h.access == nil {
		return
	}
	rec.event.DurationMs = time.Since(rec.started).Milliseconds()
	e := rec.event
	h.access.Record(&e)
}

func egressClientIP(reported string) string {
	addr, err := netip.ParseAddr(reported)
	if err != nil {
		return agentClientIP
	}
	return addr.Unmap().WithZone("").String()
}

func policyIDs(d authz.Decision) []string {
	if len(d.Policies) == 0 {
		return nil
	}
	out := make([]string, len(d.Policies))
	for i, p := range d.Policies {
		out[i] = p.ID
	}
	return out
}

func truncate(s string) string {
	const maxLen = 300
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}
