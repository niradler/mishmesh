package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/subdomain"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func (g *Gateway) handleRegister(ctx context.Context, agent *store.Agent, p *tunnel.RegisterPayload) *tunnel.RegisterAckPayload {
	ack := &tunnel.RegisterAckPayload{}
	if p == nil {
		return ack
	}
	for _, req := range p.Endpoints {
		binding, err := g.registerOne(ctx, agent, req)
		if err != nil {
			g.log.Warn("endpoint registration rejected", "agent_id", agent.ID, "ref", req.Ref, "reason", err)
			binding = tunnel.EndpointBinding{Ref: req.Ref, Kind: req.Kind, Error: err.Error()}
		}
		ack.Endpoints = append(ack.Endpoints, binding)
	}
	return ack
}

func (g *Gateway) registerOne(ctx context.Context, agent *store.Agent, req tunnel.EndpointRequest) (tunnel.EndpointBinding, error) {
	kind := req.Kind
	if kind == "" {
		kind = store.KindHTTP
	}
	lifecycle := req.Lifecycle
	if lifecycle == "" {
		lifecycle = store.LifecycleEphemeral
	}
	switch kind {
	case store.KindTCP:
		return g.registerTCP(ctx, agent, req, lifecycle)
	case store.KindHTTP, store.KindTLS:
		return g.registerHostBased(ctx, agent, req, kind, lifecycle)
	default:
		return tunnel.EndpointBinding{}, fmt.Errorf("unsupported endpoint kind %q (want http, tcp or tls)", kind)
	}
}

func (g *Gateway) kindError(kind string) error {
	if reason, ok := g.unavailable[kind]; ok {
		return errors.New(reason)
	}
	return nil
}

func (g *Gateway) registerHostBased(ctx context.Context, agent *store.Agent, req tunnel.EndpointRequest, kind, lifecycle string) (tunnel.EndpointBinding, error) {
	if err := g.kindError(kind); err != nil {
		return tunnel.EndpointBinding{}, err
	}
	sub := strings.ToLower(strings.TrimSpace(req.Subdomain))
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if sub != "" && domain != "" {
		return tunnel.EndpointBinding{}, errors.New("subdomain and domain are mutually exclusive")
	}
	if domain != "" {
		return g.rebindDomain(ctx, agent, req, kind, domain)
	}
	if sub == "" {
		sub = store.NewID("")
	} else {
		if err := subdomain.Validate(sub, ""); err != nil {
			return tunnel.EndpointBinding{}, err
		}
		existing, err := g.data.GetEndpointBySubdomain(ctx, sub)
		if err == nil {
			if existing.AgentID != agent.ID {
				return tunnel.EndpointBinding{}, fmt.Errorf("subdomain %q is already taken", sub)
			}
			if existing.Kind != kind {
				return tunnel.EndpointBinding{}, fmt.Errorf("subdomain %q is already registered as a %s endpoint", sub, existing.Kind)
			}
			return g.bindExisting(agent, req, existing), nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			g.log.Error("subdomain lookup failed", "subdomain", sub, "err", err)
			return tunnel.EndpointBinding{}, errors.New("internal error looking up subdomain")
		}
	}
	if !g.quotaAllowsEndpoint(ctx, agent.OrgID) {
		return tunnel.EndpointBinding{}, errors.New("endpoint quota exceeded for this organization")
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
		if existing := g.ownSubdomainEndpoint(ctx, agent, sub, kind); existing != nil {
			return g.bindExisting(agent, req, existing), nil
		}
		g.log.Warn("create endpoint failed", "agent_id", agent.ID, "err", err)
		return tunnel.EndpointBinding{}, errors.New("internal error creating endpoint")
	}
	g.conns.BindEndpoint(ep.ID, agent.ID)
	g.log.Info("endpoint registered", "agent_id", agent.ID, "endpoint_id", ep.ID, "kind", kind, "url", g.publicURL(ep))
	return g.binding(req.Ref, ep), nil
}

func (g *Gateway) ownSubdomainEndpoint(ctx context.Context, agent *store.Agent, sub, kind string) *store.Endpoint {
	existing, err := g.data.GetEndpointBySubdomain(ctx, sub)
	if err != nil || existing.AgentID != agent.ID || existing.Kind != kind {
		return nil
	}
	return existing
}

func (g *Gateway) rebindDomain(ctx context.Context, agent *store.Agent, req tunnel.EndpointRequest, kind, domain string) (tunnel.EndpointBinding, error) {
	existing, err := g.data.GetEndpointByDomain(ctx, domain)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return tunnel.EndpointBinding{}, fmt.Errorf("custom domain %q is not registered for this agent; create a reserved endpoint with this domain through the control API first", domain)
		}
		g.log.Error("domain lookup failed", "domain", domain, "err", err)
		return tunnel.EndpointBinding{}, errors.New("internal error looking up domain")
	}
	if existing.AgentID != agent.ID {
		return tunnel.EndpointBinding{}, fmt.Errorf("custom domain %q belongs to another agent", domain)
	}
	if existing.Kind != kind {
		return tunnel.EndpointBinding{}, fmt.Errorf("custom domain %q is registered as a %s endpoint", domain, existing.Kind)
	}
	return g.bindExisting(agent, req, existing), nil
}

func (g *Gateway) bindExisting(agent *store.Agent, req tunnel.EndpointRequest, ep *store.Endpoint) tunnel.EndpointBinding {
	g.conns.BindEndpoint(ep.ID, agent.ID)
	return g.binding(req.Ref, ep)
}

func (g *Gateway) binding(ref string, ep *store.Endpoint) tunnel.EndpointBinding {
	b := tunnel.EndpointBinding{Ref: ref, EndpointID: ep.ID, PublicURL: g.publicURL(ep), Kind: ep.Kind, Port: ep.Port}
	if ep.Kind == store.KindHTTP {
		b.PathURL = g.pathURL(ep)
	}
	return b
}

func (g *Gateway) registerTCP(ctx context.Context, agent *store.Agent, req tunnel.EndpointRequest, lifecycle string) (tunnel.EndpointBinding, error) {
	if g.ports == nil {
		if err := g.kindError(store.KindTCP); err != nil {
			return tunnel.EndpointBinding{}, err
		}
		return tunnel.EndpointBinding{}, errors.New("tcp ingress is disabled on this server")
	}
	if !g.quotaAllowsEndpoint(ctx, agent.OrgID) {
		return tunnel.EndpointBinding{}, errors.New("endpoint quota exceeded for this organization")
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
		if req.Port > 0 {
			return tunnel.EndpointBinding{}, fmt.Errorf("tcp port %d unavailable: %v", req.Port, err)
		}
		return tunnel.EndpointBinding{}, fmt.Errorf("tcp port allocation failed: %v", err)
	}
	ep.Port = port
	if err := g.data.CreateEndpoint(ctx, ep); err != nil {
		g.ports.Close(ep.ID)
		g.log.Warn("create tcp endpoint failed", "agent_id", agent.ID, "err", err)
		return tunnel.EndpointBinding{}, errors.New("internal error creating endpoint")
	}
	g.conns.BindEndpoint(ep.ID, agent.ID)
	url := fmt.Sprintf("tcp://%s:%d", g.publicHost(), port)
	g.log.Info("tcp endpoint registered", "agent_id", agent.ID, "endpoint_id", ep.ID, "url", url)
	return tunnel.EndpointBinding{Ref: req.Ref, EndpointID: ep.ID, Kind: store.KindTCP, Port: port, PublicURL: url}, nil
}

func (g *Gateway) publicURL(ep *store.Endpoint) string {
	switch {
	case ep.Domain != "":
		return fmt.Sprintf("%s://%s", g.publicScheme, ep.Domain)
	case ep.Subdomain != "":
		return fmt.Sprintf("%s://%s.%s", g.publicScheme, ep.Subdomain, g.baseDomain)
	default:
		return g.pathURL(ep)
	}
}

func (g *Gateway) pathURL(ep *store.Endpoint) string {
	return fmt.Sprintf("%s://%s/tunnel/%s", g.publicScheme, g.baseDomain, ep.ID)
}
