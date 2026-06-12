package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type Options struct {
	Data         store.DataStore
	Conns        store.ConnectionStore
	Log          *slog.Logger
	BaseDomain   string
	PublicScheme string
}

type Gateway struct {
	data         store.DataStore
	conns        store.ConnectionStore
	log          *slog.Logger
	baseDomain   string
	publicScheme string
}

func New(opts Options) *Gateway {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Gateway{
		data:         opts.Data,
		conns:        opts.Conns,
		log:          log,
		baseDomain:   opts.BaseDomain,
		publicScheme: opts.PublicScheme,
	}
}

func (g *Gateway) HandleAgentConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := bearerToken(r)
	if raw == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	tok, err := g.data.GetTokenByHash(ctx, store.HashToken(raw))
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	agent, err := g.data.GetAgent(ctx, tok.AgentID)
	if err != nil || agent.Status != store.AgentActive {
		http.Error(w, "agent not active", http.StatusForbidden)
		return
	}

	conn, err := tunnel.Accept(w, r)
	if err != nil {
		g.log.Warn("agent websocket accept failed", "agent_id", agent.ID, "err", err)
		return
	}
	sess, err := tunnel.Server(conn)
	if err != nil {
		g.log.Warn("yamux server failed", "agent_id", agent.ID, "err", err)
		_ = conn.Close()
		return
	}
	ac := newAgentConn(agent.ID, sess)
	if superseded := g.conns.AddAgent(ac); superseded != nil {
		g.log.Info("superseding existing agent connection", "agent_id", agent.ID)
		_ = superseded.Close()
	}
	_ = g.data.TouchAgent(ctx, agent.ID, time.Now())
	g.log.Info("agent connected", "agent_id", agent.ID, "org_id", agent.OrgID)

	g.serve(context.WithoutCancel(ctx), agent, ac)
}

func (g *Gateway) serve(ctx context.Context, agent *store.Agent, ac *agentConn) {
	defer func() {
		g.conns.RemoveAgent(ac)
		_ = ac.Close()
		g.cleanupEphemeral(ctx, agent.ID)
		g.log.Info("agent disconnected", "agent_id", agent.ID)
	}()

	ctrl, err := ac.sess.AcceptControl()
	if err != nil {
		return
	}
	for {
		msg, err := ctrl.Recv()
		if err != nil {
			return
		}
		switch msg.Type {
		case tunnel.MsgRegister:
			ack := g.handleRegister(ctx, agent, msg.Register)
			if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgRegisterAck, RegisterAck: ack}); err != nil {
				return
			}
		case tunnel.MsgPing:
			_ = g.data.TouchAgent(ctx, agent.ID, time.Now())
			if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgPong}); err != nil {
				return
			}
		default:
		}
	}
}

func (g *Gateway) handleRegister(ctx context.Context, agent *store.Agent, p *tunnel.RegisterPayload) *tunnel.RegisterAckPayload {
	ack := &tunnel.RegisterAckPayload{}
	if p == nil {
		return ack
	}
	for _, req := range p.Endpoints {
		kind := req.Kind
		if kind == "" {
			kind = store.KindHTTP
		}
		lifecycle := req.Lifecycle
		if lifecycle == "" {
			lifecycle = store.LifecycleEphemeral
		}
		sub := strings.ToLower(strings.TrimSpace(req.Subdomain))
		if sub == "" {
			sub = store.NewID("")
		} else if existing, err := g.data.GetEndpointBySubdomain(ctx, sub); err == nil {
			if existing.AgentID != agent.ID {
				g.log.Warn("subdomain owned by another agent", "subdomain", sub, "agent_id", agent.ID)
				ack.Endpoints = append(ack.Endpoints, tunnel.EndpointBinding{Ref: req.Ref})
				continue
			}
			g.conns.BindEndpoint(existing.ID, agent.ID)
			ack.Endpoints = append(ack.Endpoints, tunnel.EndpointBinding{
				Ref:        req.Ref,
				EndpointID: existing.ID,
				PublicURL:  g.publicURL(existing),
				Kind:       existing.Kind,
			})
			continue
		}
		ep := &store.Endpoint{
			ID:        store.NewID("ep"),
			AgentID:   agent.ID,
			OrgID:     agent.OrgID,
			Kind:      kind,
			Lifecycle: lifecycle,
			Subdomain: sub,
			CreatedAt: time.Now(),
		}
		if err := g.data.CreateEndpoint(ctx, ep); err != nil {
			g.log.Warn("create endpoint failed", "agent_id", agent.ID, "err", err)
			ack.Endpoints = append(ack.Endpoints, tunnel.EndpointBinding{Ref: req.Ref})
			continue
		}
		g.conns.BindEndpoint(ep.ID, agent.ID)
		ack.Endpoints = append(ack.Endpoints, tunnel.EndpointBinding{
			Ref:        req.Ref,
			EndpointID: ep.ID,
			PublicURL:  g.publicURL(ep),
			Kind:       ep.Kind,
		})
		g.log.Info("endpoint registered", "agent_id", agent.ID, "endpoint_id", ep.ID, "url", g.publicURL(ep))
	}
	return ack
}

func (g *Gateway) cleanupEphemeral(ctx context.Context, agentID string) {
	eps, err := g.data.ListEndpointsByAgent(ctx, agentID)
	if err != nil {
		return
	}
	for _, ep := range eps {
		if ep.Lifecycle == store.LifecycleEphemeral {
			_ = g.data.DeleteEndpoint(ctx, ep.ID)
		}
	}
}

func (g *Gateway) publicURL(ep *store.Endpoint) string {
	if ep.Subdomain != "" {
		return fmt.Sprintf("%s://%s.%s", g.publicScheme, ep.Subdomain, g.baseDomain)
	}
	return fmt.Sprintf("%s://%s/tunnel/%s", g.publicScheme, g.baseDomain, ep.ID)
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
