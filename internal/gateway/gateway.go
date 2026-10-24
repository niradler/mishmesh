package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type PortOpener interface {
	Open(endpointID string, requestedPort int) (port int, err error)
	Close(endpointID string)
}

type ConnectHandler interface {
	HandleConnect(ctx context.Context, agent *store.Agent, stream net.Conn, init tunnel.StreamInit)
}

type EgressHandler interface {
	HandleEgress(ctx context.Context, agent *store.Agent, stream net.Conn, init tunnel.StreamInit)
}

type ConnectDecider interface {
	DecideConnect(ctx context.Context, agent *store.Agent, q tunnel.DecisionQuery) tunnel.DecisionAnswer
}

type UserAuthenticator interface {
	Authenticate(ctx context.Context, raw string, allowed ...token.Type) (identity.Principal, error)
	Subscribe(fn func(identity.Revocation)) func()
}

const (
	maxPendingDecisions = 16
	maxPendingInits     = 64
	firstControlTimeout = 30 * time.Second
)

type Metrics interface {
	AgentConnected()
	AgentDisconnected()
	HandshakeFailure()
	StreamOpened(kind string)
	StreamClosed(kind string)
}

type Options struct {
	Data               store.DataStore
	Conns              store.ConnectionStore
	Log                *slog.Logger
	BaseDomain         string
	PublicScheme       string
	DisablePathRouting bool
	TLSPublicPort      int
	Ports              PortOpener
	UDPPorts           PortOpener
	Connect            ConnectHandler
	Egress             EgressHandler
	Metrics            Metrics
	Users              UserAuthenticator

	KindUnavailable map[string]string
}

type Gateway struct {
	data               store.DataStore
	conns              store.ConnectionStore
	log                *slog.Logger
	baseDomain         string
	publicScheme       string
	disablePathRouting bool
	tlsPublicPort      int
	ports              PortOpener
	udpPorts           PortOpener
	connect            ConnectHandler
	egress             EgressHandler
	metrics            Metrics
	users              UserAuthenticator
	unavailable        map[string]string

	liveMu sync.Mutex
	live   map[*agentConn]struct{}
}

func New(opts Options) *Gateway {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	g := &Gateway{
		data:               opts.Data,
		conns:              opts.Conns,
		log:                log,
		baseDomain:         opts.BaseDomain,
		publicScheme:       opts.PublicScheme,
		disablePathRouting: opts.DisablePathRouting,
		tlsPublicPort:      opts.TLSPublicPort,
		ports:              opts.Ports,
		udpPorts:           opts.UDPPorts,
		connect:            opts.Connect,
		egress:             opts.Egress,
		metrics:            opts.Metrics,
		users:              opts.Users,
		unavailable:        opts.KindUnavailable,
		live:               make(map[*agentConn]struct{}),
	}
	if g.users != nil {
		g.users.Subscribe(g.revoke)
	}
	return g
}

func (g *Gateway) revoke(rev identity.Revocation) {
	g.liveMu.Lock()
	var hit []*agentConn
	for ac := range g.live {
		if ac.boundOrg == rev.OrgID && ac.boundSession != "" && slices.Contains(rev.SessionIDs, ac.boundSession) {
			hit = append(hit, ac)
		}
	}
	g.liveMu.Unlock()
	for _, ac := range hit {
		_ = ac.Flush()
		g.log.Info("bound user session revoked; agent decisions flushed", "agent_id", ac.agentID)
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
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		g.unavailable503(w, "token lookup failed", err)
		return
	}
	agent, err := g.data.GetAgent(ctx, tok.AgentID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		g.unavailable503(w, "agent lookup failed", err)
		return
	}
	if err != nil || agent.Status != store.AgentActive {
		http.Error(w, "agent not active", http.StatusForbidden)
		return
	}
	user, ok := g.bindUser(w, r, agent)
	if !ok {
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
	ac := newAgentConn(agent.ID, agent.OrgID, sess, g.metrics)
	if user != nil {
		ac.boundOrg, ac.boundSession = user.OrgID, user.SessionID
	}
	if superseded := g.conns.AddAgent(ac); superseded != nil {
		g.log.Info("superseding existing agent connection", "agent_id", agent.ID)
		_ = superseded.Close()
	}
	_ = g.data.TouchAgent(ctx, agent.ID, time.Now())
	if g.metrics != nil {
		g.metrics.AgentConnected()
	}
	serveCtx := context.WithoutCancel(ctx)
	if user != nil {
		serveCtx = identity.WithPrincipal(serveCtx, *user)
		g.log.Info("agent connected", "agent_id", agent.ID, "org_id", agent.OrgID, "user_id", user.Ref.ID)
	} else {
		g.log.Info("agent connected", "agent_id", agent.ID, "org_id", agent.OrgID)
	}

	g.serve(serveCtx, agent, ac)
}

func (g *Gateway) bindUser(w http.ResponseWriter, r *http.Request, agent *store.Agent) (*identity.Principal, bool) {
	raw := strings.TrimSpace(r.Header.Get(tunnel.UserTokenHeader))
	if raw == "" {
		return nil, true
	}
	if g.users == nil {
		http.Error(w, "user binding is not available on this gateway", http.StatusUnauthorized)
		return nil, false
	}
	p, err := g.users.Authenticate(r.Context(), raw, token.TypeAgentUser)
	switch {
	case errors.Is(err, identity.ErrUnavailable):
		g.unavailable503(w, "user token lookup failed", err)
		return nil, false
	case err != nil:
		http.Error(w, "invalid user token", http.StatusUnauthorized)
		return nil, false
	case p.OrgID != agent.OrgID:
		http.Error(w, "user token belongs to another organization", http.StatusForbidden)
		return nil, false
	}
	return &p, true
}

func (g *Gateway) FlushAll() {
	g.FlushOrg("")
}

func (g *Gateway) FlushOrg(orgID string) {
	g.liveMu.Lock()
	conns := make([]*agentConn, 0, len(g.live))
	for ac := range g.live {
		if orgID == "" || ac.orgID == orgID {
			conns = append(conns, ac)
		}
	}
	g.liveMu.Unlock()
	for _, ac := range conns {
		if err := ac.Flush(); err != nil {
			g.log.Debug("flush agent decision cache failed", "agent_id", ac.agentID, "err", err)
		}
	}
}

func (g *Gateway) track(ac *agentConn) func() {
	g.liveMu.Lock()
	g.live[ac] = struct{}{}
	g.liveMu.Unlock()
	return func() {
		g.liveMu.Lock()
		delete(g.live, ac)
		g.liveMu.Unlock()
	}
}

func (g *Gateway) recoverPanic(where, agentID string) {
	if v := recover(); v != nil {
		g.log.Error("recovered panic", "where", where, "agent_id", agentID, "panic", v, "stack", string(debug.Stack()))
	}
}

func (g *Gateway) unavailable503(w http.ResponseWriter, msg string, err error) {
	g.log.Error("agent connect: "+msg, "err", err)
	w.Header().Set("Retry-After", "1")
	http.Error(w, "temporarily unavailable, retry shortly", http.StatusServiceUnavailable)
}

func (g *Gateway) serve(ctx context.Context, agent *store.Agent, ac *agentConn) {
	untrack := g.track(ac)
	defer func() {
		untrack()
		g.conns.RemoveAgent(ac)
		_ = ac.Close()
		if g.agentHasLiveSession(agent.ID) {
			g.log.Info("agent has a newer live session; skipping endpoint cleanup", "agent_id", agent.ID)
		} else {
			g.cleanupEphemeral(ctx, agent.ID)
		}
		if g.metrics != nil {
			g.metrics.AgentDisconnected()
		}
		g.log.Info("agent disconnected", "agent_id", agent.ID)
	}()
	defer g.recoverPanic("serve", agent.ID)

	ctrl, err := g.acceptControl(ac)
	if err != nil {
		return
	}
	ac.setControl(ctrl)
	go g.acceptConnects(ctx, agent, ac)
	decisions := make(chan struct{}, maxPendingDecisions)
	for {
		msg, err := ctrl.Recv()
		if err != nil {
			return
		}
		switch msg.Type {
		case tunnel.MsgDecisionRequest:
			if msg.Decision != nil {
				g.answerDecision(ctx, agent, ctrl, *msg.Decision, decisions)
			}
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

func (g *Gateway) cleanupEphemeral(ctx context.Context, agentID string) {
	eps, err := g.data.ListEndpointsByAgent(ctx, agentID)
	if err != nil {
		return
	}
	for _, ep := range eps {
		if g.agentHasLiveSession(agentID) {
			return
		}
		if ports := g.portsFor(ep.Kind); store.PortKind(ep.Kind) && ep.Port > 0 && ports != nil {
			ports.Close(ep.ID)
		}
		if ep.Lifecycle == store.LifecycleEphemeral {
			_ = g.data.DeleteEndpoint(ctx, ep.ID)
		}
	}
}

func (g *Gateway) acceptControl(ac *agentConn) (*tunnel.Control, error) {
	type result struct {
		ctrl *tunnel.Control
		err  error
	}
	done := make(chan result, 1)
	go func() {
		ctrl, err := ac.sess.AcceptControl()
		done <- result{ctrl, err}
	}()
	select {
	case r := <-done:
		return r.ctrl, r.err
	case <-time.After(firstControlTimeout):
		_ = ac.Close()
		return nil, errors.New("gateway: agent did not open a control stream in time")
	}
}

func (g *Gateway) answerDecision(ctx context.Context, agent *store.Agent, ctrl *tunnel.Control, q tunnel.DecisionQuery, slots chan struct{}) {
	decider, ok := g.connect.(ConnectDecider)
	if !ok {
		_ = ctrl.Send(denyAnswer(q.ID, "unsupported"))
		return
	}
	select {
	case slots <- struct{}{}:
	default:
		_ = ctrl.Send(denyAnswer(q.ID, "busy"))
		return
	}
	go func() {
		defer func() { <-slots }()
		defer g.recoverPanic("decision", agent.ID)
		answer := decider.DecideConnect(ctx, agent, q)
		answer.ID = q.ID
		answer.TTLSeconds = min(max(answer.TTLSeconds, 0), tunnel.MaxDecisionTTLSeconds)
		_ = ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgDecisionResponse, Answer: &answer})
	}()
}

func denyAnswer(id uint64, reason string) tunnel.ControlMessage {
	return tunnel.ControlMessage{Type: tunnel.MsgDecisionResponse, Answer: &tunnel.DecisionAnswer{ID: id, Reason: reason}}
}

func (g *Gateway) acceptConnects(ctx context.Context, agent *store.Agent, ac *agentConn) {
	defer g.recoverPanic("accept_connects", agent.ID)
	pending := make(chan struct{}, maxPendingInits)
	for {
		stream, err := ac.sess.AcceptStream()
		if err != nil {
			return
		}
		select {
		case pending <- struct{}{}:
		default:
			g.log.Warn("rejecting agent-opened stream: too many pending stream inits", "agent_id", agent.ID)
			_ = stream.Close()
			continue
		}
		go g.handleAgentStream(ctx, agent, stream, pending)
	}
}

func (g *Gateway) handleAgentStream(ctx context.Context, agent *store.Agent, stream net.Conn, pending chan struct{}) {
	defer g.recoverPanic("agent_stream", agent.ID)
	init, err := tunnel.ReadStreamInit(stream)
	<-pending
	if err != nil {
		return
	}
	connectRef, egressDest := init.Meta[tunnel.MetaConnect], init.Meta[tunnel.MetaEgress]
	switch {
	case connectRef != "" && egressDest == "" && g.connect != nil:
		g.connect.HandleConnect(ctx, agent, stream, init)
	case egressDest != "" && connectRef == "" && g.egress != nil:
		g.egress.HandleEgress(ctx, agent, stream, init)
	default:
		g.log.Warn("rejecting agent-opened stream", "agent_id", agent.ID, "kind", init.Kind, "connect_enabled", g.connect != nil, "egress_enabled", g.egress != nil)
		_ = stream.Close()
	}
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
	if p.RateLimit != nil {
		if err := p.RateLimit.Validate(); err != nil {
			log.Warn("ignoring invalid endpoint rate limit", "err", err)
			p.RateLimit = nil
		}
	}
	if dropped := dropControlPlaneFields(&p); len(dropped) > 0 {
		log.Warn("ignoring endpoint policy fields only the control API may set", "fields", dropped)
	}
	return &p
}

func dropControlPlaneFields(p *store.EndpointPolicy) []string {
	var dropped []string
	if len(p.ProxyTargets) > 0 {
		dropped = append(dropped, "proxy_targets")
		p.ProxyTargets = nil
	}
	if p.LoadBalance != "" {
		dropped = append(dropped, "load_balance")
		p.LoadBalance = ""
	}
	if p.OriginTLS != nil {
		dropped = append(dropped, "origin_tls")
		p.OriginTLS = nil
	}
	if p.ServiceID != "" {
		dropped = append(dropped, "service_id")
		p.ServiceID = ""
	}
	if len(p.Access) > 0 {
		dropped = append(dropped, "access")
		p.Access = nil
	}
	return dropped
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
