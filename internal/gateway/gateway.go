package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type PortOpener interface {
	Open(endpointID string, requestedPort int) (port int, err error)
	Close(endpointID string)
}

type Metrics interface {
	AgentConnected()
	AgentDisconnected()
	HandshakeFailure()
	StreamOpened(kind string)
}

type Options struct {
	Data         store.DataStore
	Conns        store.ConnectionStore
	Log          *slog.Logger
	BaseDomain   string
	PublicScheme string
	Ports        PortOpener
	Metrics      Metrics
}

type Gateway struct {
	data         store.DataStore
	conns        store.ConnectionStore
	log          *slog.Logger
	baseDomain   string
	publicScheme string
	ports        PortOpener
	metrics      Metrics
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
		ports:        opts.Ports,
		metrics:      opts.Metrics,
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
		if g.metrics != nil {
			g.metrics.HandshakeFailure()
		}
		return
	}
	sess, err := tunnel.Server(conn)
	if err != nil {
		g.log.Warn("yamux server failed", "agent_id", agent.ID, "err", err)
		if g.metrics != nil {
			g.metrics.HandshakeFailure()
		}
		_ = conn.Close()
		return
	}
	ac := newAgentConn(agent.ID, sess, g.metrics)
	if superseded := g.conns.AddAgent(ac); superseded != nil {
		g.log.Info("superseding existing agent connection", "agent_id", agent.ID)
		_ = superseded.Close()
	}
	_ = g.data.TouchAgent(ctx, agent.ID, time.Now())
	if g.metrics != nil {
		g.metrics.AgentConnected()
	}
	g.log.Info("agent connected", "agent_id", agent.ID, "org_id", agent.OrgID)

	g.serve(context.WithoutCancel(ctx), agent, ac)
}

func (g *Gateway) serve(ctx context.Context, agent *store.Agent, ac *agentConn) {
	defer func() {
		g.conns.RemoveAgent(ac)
		_ = ac.Close()
		g.cleanupEphemeral(ctx, agent.ID)
		if g.metrics != nil {
			g.metrics.AgentDisconnected()
		}
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
		if kind == store.KindTCP {
			ack.Endpoints = append(ack.Endpoints, g.registerTCP(ctx, agent, req, lifecycle))
			continue
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
		if !g.quotaAllowsEndpoint(ctx, agent.OrgID) {
			g.log.Warn("endpoint quota exceeded", "agent_id", agent.ID, "org_id", agent.OrgID)
			ack.Endpoints = append(ack.Endpoints, tunnel.EndpointBinding{Ref: req.Ref})
			continue
		}
		ep := &store.Endpoint{
			ID:        store.NewID("ep"),
			AgentID:   agent.ID,
			OrgID:     agent.OrgID,
			Kind:      kind,
			Lifecycle: lifecycle,
			Subdomain: sub,
			Policy:    decodePolicy(req.Policy, g.log),
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

func (g *Gateway) registerTCP(ctx context.Context, agent *store.Agent, req tunnel.EndpointRequest, lifecycle string) tunnel.EndpointBinding {
	if g.ports == nil {
		g.log.Warn("tcp endpoint requested but tcp ingress is disabled", "agent_id", agent.ID)
		return tunnel.EndpointBinding{Ref: req.Ref}
	}
	if !g.quotaAllowsEndpoint(ctx, agent.OrgID) {
		g.log.Warn("endpoint quota exceeded", "agent_id", agent.ID, "org_id", agent.OrgID)
		return tunnel.EndpointBinding{Ref: req.Ref}
	}
	ep := &store.Endpoint{
		ID:        store.NewID("ep"),
		AgentID:   agent.ID,
		OrgID:     agent.OrgID,
		Kind:      store.KindTCP,
		Lifecycle: lifecycle,
		Policy:    decodePolicy(req.Policy, g.log),
		CreatedAt: time.Now(),
	}
	port, err := g.ports.Open(ep.ID, req.Port)
	if err != nil {
		g.log.Warn("tcp port allocation failed", "agent_id", agent.ID, "err", err)
		return tunnel.EndpointBinding{Ref: req.Ref}
	}
	ep.Port = port
	if err := g.data.CreateEndpoint(ctx, ep); err != nil {
		g.ports.Close(ep.ID)
		g.log.Warn("create tcp endpoint failed", "agent_id", agent.ID, "err", err)
		return tunnel.EndpointBinding{Ref: req.Ref}
	}
	g.conns.BindEndpoint(ep.ID, agent.ID)
	url := fmt.Sprintf("tcp://%s:%d", g.publicHost(), port)
	g.log.Info("tcp endpoint registered", "agent_id", agent.ID, "endpoint_id", ep.ID, "url", url)
	return tunnel.EndpointBinding{Ref: req.Ref, EndpointID: ep.ID, Kind: store.KindTCP, Port: port, PublicURL: url}
}

func (g *Gateway) cleanupEphemeral(ctx context.Context, agentID string) {
	eps, err := g.data.ListEndpointsByAgent(ctx, agentID)
	if err != nil {
		return
	}
	for _, ep := range eps {
		if ep.Kind == store.KindTCP && g.ports != nil {
			g.ports.Close(ep.ID)
		}
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

func (g *Gateway) publicHost() string {
	if h, _, err := net.SplitHostPort(g.baseDomain); err == nil {
		return h
	}
	return g.baseDomain
}

func (g *Gateway) quotaAllowsEndpoint(ctx context.Context, orgID string) bool {
	q, err := g.data.GetQuota(ctx, orgID)
	if err != nil || q.MaxEndpoints <= 0 {
		return true
	}
	n, err := g.data.CountEndpoints(ctx, orgID)
	if err != nil {
		return true
	}
	return n < q.MaxEndpoints
}

func decodePolicy(raw json.RawMessage, log *slog.Logger) *store.EndpointPolicy {
	if len(raw) == 0 {
		return nil
	}
	var p store.EndpointPolicy
	if err := json.Unmarshal(raw, &p); err != nil {
		log.Warn("ignoring invalid endpoint policy", "err", err)
		return nil
	}
	return &p
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
