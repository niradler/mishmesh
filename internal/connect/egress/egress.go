package egress

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/flows"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	MeterKind       = "egress"
	decisionTimeout = 5 * time.Second
	dialTimeout     = 10 * time.Second
	replyTimeout    = 5 * time.Second
	agentClientIP   = "0.0.0.0"
	reasonBlocked   = "blocked_destination"
	reasonDial      = "dial_failed"
	reasonSNI       = "sni_mismatch"
)

type Dialer interface {
	Dial(ctx context.Context, target string, otls *store.OriginTLS, timeout time.Duration) (net.Conn, error)
}

type Meter interface {
	AddBytes(kind string, in, out int64)
}

type Recorder interface {
	Record(e *store.AccessEvent)
}

type Options struct {
	Conns       store.ConnectionStore
	Log         *slog.Logger
	Meter       Meter
	Deciders    identity.Deciders
	Dialer      Dialer
	Access      Recorder
	Revocations flows.Revocations
}

type Handler struct {
	conns    store.ConnectionStore
	log      *slog.Logger
	meter    Meter
	deciders identity.Deciders
	dialer   Dialer
	access   Recorder
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
		flows:    flows.NewTracker(log, "egress", opts.Revocations),
	}
}

type flowRecord struct {
	event   store.AccessEvent
	started time.Time
}

func (h *Handler) HandleEgress(ctx context.Context, agent *store.Agent, stream net.Conn, init tunnel.StreamInit) {
	defer stream.Close()
	rec := &flowRecord{started: time.Now(), event: store.AccessEvent{
		OrgID:         agent.OrgID,
		Source:        store.AccessSourceEgress,
		AgentID:       agent.ID,
		PrincipalType: string(authz.EntityAgent),
		PrincipalID:   agent.ID,
		ClientIP:      agentClientIP,
		Protocol:      "tcp",
		Destination:   truncate(init.Meta[tunnel.MetaEgress]),
	}}
	p, hasPrincipal := identity.PrincipalFrom(ctx)
	if hasPrincipal {
		rec.event.PrincipalType = string(p.Ref.Type)
		rec.event.PrincipalID = p.Ref.ID
		rec.event.SessionID = p.SessionID
	}

	dest, err := authz.NormalizeDestination(init.Meta[tunnel.MetaEgress])
	if err != nil {
		h.refuse(stream, rec, tunnel.EgressInvalid, string(authz.ReasonInvalidRequest))
		return
	}
	rec.event.Destination = dest
	decision, status := h.decide(ctx, agent, p, hasPrincipal, dest)
	rec.event.PolicyIDs = policyIDs(decision)
	if status != tunnel.EgressOK {
		h.refuse(stream, rec, status, string(decision.Reason))
		return
	}

	var flow *flows.Flow
	if hasPrincipal {
		var untrack func()
		flow, untrack = h.flows.Track(agent.OrgID, p.SessionID, stream)
		defer untrack()
	}
	upstream, err := h.dialer.Dial(ctx, dest, nil, dialTimeout)
	if err != nil {
		status, reason := tunnel.EgressDialFailed, reasonDial
		if errors.Is(err, proxy.ErrBlocked) {
			status, reason = tunnel.EgressDenied, reasonBlocked
		}
		h.log.Info("egress dial refused", "agent_id", agent.ID, "destination", dest, "err", err)
		h.refuse(stream, rec, status, reason)
		return
	}
	defer upstream.Close()
	if flow != nil && !h.flows.Attach(flow, upstream) {
		h.refuse(stream, rec, tunnel.EgressDenied, string(authz.ReasonSessionRevoked))
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

func (h *Handler) decide(ctx context.Context, agent *store.Agent, p identity.Principal, hasPrincipal bool, dest string) (authz.Decision, tunnel.EgressStatus) {
	if h.deciders == nil || h.dialer == nil {
		return authz.Decision{Reason: authz.ReasonUnavailable}, tunnel.EgressUnavailable
	}
	scope := identity.Scope{OrgID: agent.OrgID, AgentID: agent.ID}
	principal := authz.EntityRef{Type: authz.EntityAgent, ID: agent.ID}
	reqCtx := authz.RequestContext{ClientIP: agentClientIP, AuthMethod: authz.AuthMethodAgentToken}
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

func (h *Handler) refuse(stream net.Conn, rec *flowRecord, status tunnel.EgressStatus, reason string) {
	h.reply(stream, tunnel.EgressReply{Status: status, Reason: reason})
	rec.event.Decision = store.AccessDeny
	rec.event.Reason = reason
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
