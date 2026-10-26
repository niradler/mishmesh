package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/endpointurl"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/subdomain"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func (g *Gateway) handleRegister(ctx context.Context, agent *store.Agent, p *tunnel.RegisterPayload) *tunnel.RegisterAckPayload {
	ack := &tunnel.RegisterAckPayload{OrgID: agent.OrgID, AgentID: agent.ID}
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
	case store.KindTCP, store.KindUDP:
		if req.Private {
			return g.registerPrivate(ctx, agent, req, kind, lifecycle)
		}
		return g.registerPort(ctx, agent, req, kind, lifecycle)
	case store.KindHTTP, store.KindTLS:
		if req.Private {
			return tunnel.EndpointBinding{}, fmt.Errorf("private applies only to tcp and udp endpoints, not %s", kind)
		}
		return g.registerHostBased(ctx, agent, req, kind, lifecycle)
	default:
		return tunnel.EndpointBinding{}, fmt.Errorf("unsupported endpoint kind %q (want http, tcp, tls or udp)", kind)
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
	pol := decodePolicy(req.Policy, g.log)
	if _, err := g.connectNameOwner(ctx, agent, connectName(pol)); err != nil {
		return tunnel.EndpointBinding{}, err
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
	ep, err := g.createEndpoint(ctx, agent, endpointSpec{kind: kind, lifecycle: lifecycle, subdomain: sub, policy: pol}, nil)
	if err != nil {
		if existing := g.ownSubdomainEndpoint(ctx, agent, sub, kind); existing != nil && !isQuotaError(err) {
			return g.bindExisting(agent, req, existing), nil
		}
		return tunnel.EndpointBinding{}, err
	}
	return g.binding(req.Ref, ep), nil
}

type endpointSpec struct {
	kind      string
	lifecycle string
	subdomain string
	policy    *store.EndpointPolicy
}

func isQuotaError(err error) bool {
	return errors.Is(err, errEndpointQuotaExceeded) || errors.Is(err, errEndpointQuotaCheck)
}

func (g *Gateway) createEndpoint(ctx context.Context, agent *store.Agent, spec endpointSpec, claimPort func(*store.Endpoint) error) (*store.Endpoint, error) {
	if err := g.checkEndpointQuota(ctx, agent.OrgID); err != nil {
		return nil, err
	}
	ep := &store.Endpoint{
		ID:        store.NewID("ep"),
		AgentID:   agent.ID,
		OrgID:     agent.OrgID,
		Kind:      spec.kind,
		Lifecycle: spec.lifecycle,
		Subdomain: spec.subdomain,
		Policy:    spec.policy,
		CreatedAt: time.Now(),
	}
	if claimPort != nil {
		if err := claimPort(ep); err != nil {
			return nil, err
		}
	}
	if err := g.data.CreateEndpoint(ctx, ep); err != nil {
		if ep.Port != 0 {
			g.portsFor(ep.Kind).Close(ep.ID)
		}
		g.log.Warn("create endpoint failed", "agent_id", agent.ID, "kind", ep.Kind, "err", err)
		return nil, errors.New("internal error creating endpoint")
	}
	g.notifyEndpointsChanged()
	g.conns.BindEndpoint(ep.ID, agent.ID)
	g.log.Info("endpoint registered", "agent_id", agent.ID, "endpoint_id", ep.ID, "kind", ep.Kind, "url", g.publicURL(ep))
	return ep, nil
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

func (g *Gateway) portsFor(kind string) PortOpener {
	if kind == store.KindUDP {
		return g.udpPorts
	}
	return g.ports
}

func (g *Gateway) endpointPolicy(req tunnel.EndpointRequest) *store.EndpointPolicy {
	pol := decodePolicy(req.Policy, g.log)
	if req.Private {
		if pol == nil {
			pol = &store.EndpointPolicy{}
		}
		if pol.Connect == nil {
			pol.Connect = &store.ConnectPolicy{}
		}
	}
	if pol != nil && pol.Connect != nil && pol.Connect.Name == "" {
		pol.Connect.Name = req.Name
	}
	return pol
}

func connectName(pol *store.EndpointPolicy) string {
	if pol == nil || pol.Connect == nil {
		return ""
	}
	return pol.Connect.Name
}

func (g *Gateway) connectNameOwner(ctx context.Context, agent *store.Agent, name string) (*store.Endpoint, error) {
	if name == "" {
		return nil, nil
	}
	ep, err := store.FindConnectEndpoint(ctx, g.data, agent.OrgID, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		g.log.Error("connect name lookup failed", "name", name, "err", err)
		return nil, errors.New("internal error looking up connect name")
	}
	if ep.AgentID != agent.ID {
		return nil, fmt.Errorf("connect name %q is already used by another agent in this organization", name)
	}
	return ep, nil
}

func (g *Gateway) registerPrivate(ctx context.Context, agent *store.Agent, req tunnel.EndpointRequest, kind, lifecycle string) (tunnel.EndpointBinding, error) {
	pol := g.endpointPolicy(req)
	name := connectName(pol)
	if name == "" {
		return tunnel.EndpointBinding{}, errors.New("private endpoints need a name (the tunnel name or policy.connect.name)")
	}
	if len(pol.Connect.AllowAgents) == 0 {
		g.log.Warn("private endpoint allows no agents to connect", "agent_id", agent.ID, "name", name)
	}
	existing, err := g.connectNameOwner(ctx, agent, name)
	if err != nil {
		return tunnel.EndpointBinding{}, err
	}
	if existing != nil {
		if existing.Kind != kind || existing.Port != 0 {
			return tunnel.EndpointBinding{}, fmt.Errorf("connect name %q is already registered as a public %s endpoint", name, existing.Kind)
		}
		existing.Policy = mergeAgentPolicy(existing.Policy, pol)
		if err := g.data.UpdateEndpoint(ctx, existing); err != nil {
			g.log.Warn("update private endpoint failed", "endpoint_id", existing.ID, "err", err)
		}
		return g.bindExisting(agent, req, existing), nil
	}
	ep, err := g.createEndpoint(ctx, agent, endpointSpec{kind: kind, lifecycle: lifecycle, policy: pol}, nil)
	if err != nil {
		return tunnel.EndpointBinding{}, err
	}
	return g.binding(req.Ref, ep), nil
}

func (g *Gateway) registerPort(ctx context.Context, agent *store.Agent, req tunnel.EndpointRequest, kind, lifecycle string) (tunnel.EndpointBinding, error) {
	ports := g.portsFor(kind)
	if ports == nil {
		if err := g.kindError(kind); err != nil {
			return tunnel.EndpointBinding{}, err
		}
		return tunnel.EndpointBinding{}, fmt.Errorf("%s ingress is disabled on this server", kind)
	}
	pol := g.endpointPolicy(req)
	if _, err := g.connectNameOwner(ctx, agent, connectName(pol)); err != nil {
		return tunnel.EndpointBinding{}, err
	}
	if req.Port > 0 {
		endpoints, err := g.data.ListEndpointsByAgent(ctx, agent.ID)
		if err != nil {
			g.log.Warn("port endpoint lookup failed", "agent_id", agent.ID, "kind", kind, "err", err)
			return tunnel.EndpointBinding{}, fmt.Errorf("internal error looking up %s endpoint", kind)
		}
		for _, endpoint := range endpoints {
			if endpoint.Kind != kind || endpoint.Port != req.Port {
				continue
			}
			if _, err := ports.Open(endpoint.ID, endpoint.Port); err != nil {
				return tunnel.EndpointBinding{}, fmt.Errorf("%s port %d unavailable: %v", kind, req.Port, err)
			}
			return g.bindExisting(agent, req, endpoint), nil
		}
	}
	openPort := func(ep *store.Endpoint) error {
		port, err := ports.Open(ep.ID, req.Port)
		if err != nil {
			if req.Port > 0 {
				return fmt.Errorf("%s port %d unavailable: %v", kind, req.Port, err)
			}
			return fmt.Errorf("%s port allocation failed: %v", kind, err)
		}
		ep.Port = port
		return nil
	}
	ep, err := g.createEndpoint(ctx, agent, endpointSpec{kind: kind, lifecycle: lifecycle, policy: pol}, openPort)
	if err != nil {
		return tunnel.EndpointBinding{}, err
	}
	return g.binding(req.Ref, ep), nil
}

func (g *Gateway) publicURL(ep *store.Endpoint) string {
	switch {
	case ep.Kind == store.KindTLS:
		return endpointurl.TLS(ep, g.baseDomain, g.tlsPublicPort)
	case store.PortKind(ep.Kind):
		if ep.Port == 0 {
			return ""
		}
		return fmt.Sprintf("%s://%s:%d", ep.Kind, g.publicHost(), ep.Port)
	case ep.Domain != "":
		return fmt.Sprintf("%s://%s", g.publicScheme, ep.Domain)
	case ep.Subdomain != "":
		return fmt.Sprintf("%s://%s.%s", g.publicScheme, ep.Subdomain, g.baseDomain)
	default:
		return g.pathURL(ep)
	}
}

func (g *Gateway) pathURL(ep *store.Endpoint) string {
	if g.disablePathRouting {
		return ""
	}
	return fmt.Sprintf("%s://%s/tunnel/%s", g.publicScheme, g.baseDomain, ep.ID)
}
