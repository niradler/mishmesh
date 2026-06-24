package sshfwd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mishmesh/mishmesh/internal/store"
)

type PortOpener interface {
	Open(endpointID string, requestedPort int) (port int, err error)
	Close(endpointID string)
}

type Metrics interface {
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
	HostKeyPEM   []byte
}

type Server struct {
	data         store.DataStore
	conns        store.ConnectionStore
	log          *slog.Logger
	baseDomain   string
	publicScheme string
	ports        PortOpener
	metrics      Metrics
	sshConfig    *ssh.ServerConfig

	ln     net.Listener
	mu     sync.Mutex
	closed bool
}

func New(opts Options) (*Server, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	signer, err := hostSigner(opts.HostKeyPEM)
	if err != nil {
		return nil, err
	}
	s := &Server{
		data:         opts.Data,
		conns:        opts.Conns,
		log:          log,
		baseDomain:   opts.BaseDomain,
		publicScheme: opts.PublicScheme,
		ports:        opts.Ports,
		metrics:      opts.Metrics,
	}
	cfg := &ssh.ServerConfig{PasswordCallback: s.authPassword}
	cfg.AddHostKey(signer)
	s.sshConfig = cfg
	return s, nil
}

func hostSigner(pemBytes []byte) (ssh.Signer, error) {
	if len(pemBytes) > 0 {
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("sshfwd: parse host key: %w", err)
		}
		return signer, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sshfwd: generate host key: %w", err)
	}
	return ssh.NewSignerFromKey(priv)
}

func (s *Server) authPassword(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := s.data.GetTokenByHash(ctx, store.HashToken(string(pass)))
	if err != nil {
		return nil, fmt.Errorf("sshfwd: invalid token")
	}
	agent, err := s.data.GetAgent(ctx, tok.AgentID)
	if err != nil || agent.Status != store.AgentActive {
		return nil, fmt.Errorf("sshfwd: agent not active")
	}
	return &ssh.Permissions{Extensions: map[string]string{"org_id": agent.OrgID}}, nil
}

func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	for {
		nConn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		go s.handleConn(nConn)
	}
}

func (s *Server) Listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() { _ = s.Serve(ln) }()
	return ln, nil
}

func (s *Server) Shutdown() {
	s.mu.Lock()
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (s *Server) handleConn(nConn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, s.sshConfig)
	if err != nil {
		_ = nConn.Close()
		return
	}
	orgID := sshConn.Permissions.Extensions["org_id"]
	ctx := context.WithoutCancel(context.Background())

	ag := &store.Agent{ID: store.NewID("ag"), OrgID: orgID, Name: "ssh:" + sshConn.User(), Status: store.AgentActive, CreatedAt: time.Now()}
	if err := s.data.CreateAgent(ctx, ag); err != nil {
		s.log.Warn("sshfwd: create session agent failed", "err", err)
		_ = sshConn.Close()
		return
	}

	ac := newAgentConn(ag.ID, sshConn, s.metrics)
	if superseded := s.conns.AddAgent(ac); superseded != nil {
		_ = superseded.Close()
	}
	s.log.Info("ssh session connected", "agent_id", ag.ID, "org_id", orgID, "user", sshConn.User())

	defer func() {
		s.conns.RemoveAgent(ac)
		s.cleanup(ctx, ag.ID)
		_ = s.data.DeleteAgent(ctx, ag.ID)
		_ = sshConn.Close()
		s.log.Info("ssh session disconnected", "agent_id", ag.ID)
	}()

	go s.serveChannels(ac, chans)
	s.serveGlobalRequests(ctx, ag, orgID, ac, reqs)
}

func (s *Server) serveChannels(ac *agentConn, chans <-chan ssh.NewChannel) {
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only remote forwarding is supported")
			continue
		}
		ch, reqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.serveSession(ac, ch, reqs)
	}
}

func (s *Server) serveSession(ac *agentConn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	ac.addSession(ch)
	defer ac.removeSession(ch)
	const banner = "mishmesh: connected. your public endpoints (live while this session stays open):\r\n"
	for req := range reqs {
		switch req.Type {
		case "shell", "pty-req", "env":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			if req.Type == "shell" {
				_, _ = ch.Write([]byte(banner))
			}
		case "exec":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			_, _ = ch.Write([]byte(banner))
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
			_ = ch.Close()
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

type forwardRequest struct {
	BindAddr string
	BindPort uint32
}

type forwardReply struct {
	BoundPort uint32
}

func (s *Server) serveGlobalRequests(ctx context.Context, ag *store.Agent, orgID string, ac *agentConn, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			var fr forwardRequest
			if err := ssh.Unmarshal(req.Payload, &fr); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			ep, boundPort, ok := s.provisionForward(ctx, ag, orgID, ac, fr)
			if !ok {
				_ = req.Reply(false, nil)
				continue
			}
			ac.setForward(ep.ID, fr.BindAddr, boundPort)
			ac.announce(s.publicURL(ep))
			s.log.Info("ssh remote forward bound", "agent_id", ag.ID, "endpoint_id", ep.ID, "url", s.publicURL(ep))
			if req.WantReply {
				_ = req.Reply(true, ssh.Marshal(forwardReply{BoundPort: uint32(boundPort)}))
			}
		case "cancel-tcpip-forward":
			var fr forwardRequest
			if err := ssh.Unmarshal(req.Payload, &fr); err == nil {
				s.cancelForward(ctx, ac, fr)
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		case "keepalive@openssh.com":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func (s *Server) provisionForward(ctx context.Context, ag *store.Agent, orgID string, ac *agentConn, fr forwardRequest) (*store.Endpoint, int, bool) {
	if !s.quotaAllowsEndpoint(ctx, orgID) {
		s.log.Warn("sshfwd: endpoint quota exceeded", "org_id", orgID)
		return nil, 0, false
	}
	if isHTTPPort(fr.BindPort) {
		sub := s.allocSubdomain(ctx, ag, ac.user)
		ep := &store.Endpoint{
			ID: store.NewID("ep"), AgentID: ag.ID, OrgID: orgID, Kind: store.KindHTTP, Method: store.MethodSSH,
			Lifecycle: store.LifecycleEphemeral, Subdomain: sub, CreatedAt: time.Now(),
		}
		if err := s.data.CreateEndpoint(ctx, ep); err != nil {
			s.log.Warn("sshfwd: create http endpoint failed", "err", err)
			return nil, 0, false
		}
		s.conns.BindEndpoint(ep.ID, ag.ID)
		return ep, int(fr.BindPort), true
	}
	if s.ports == nil {
		s.log.Warn("sshfwd: tcp forward requested but tcp ingress disabled", "agent_id", ag.ID)
		return nil, 0, false
	}
	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: ag.ID, OrgID: orgID, Kind: store.KindTCP, Method: store.MethodSSH,
		Lifecycle: store.LifecycleEphemeral, CreatedAt: time.Now(),
	}
	port, err := s.ports.Open(ep.ID, int(fr.BindPort))
	if err != nil {
		s.log.Warn("sshfwd: tcp port allocation failed", "err", err)
		return nil, 0, false
	}
	ep.Port = port
	if err := s.data.CreateEndpoint(ctx, ep); err != nil {
		s.ports.Close(ep.ID)
		return nil, 0, false
	}
	s.conns.BindEndpoint(ep.ID, ag.ID)
	return ep, port, true
}

func (s *Server) cancelForward(ctx context.Context, ac *agentConn, fr forwardRequest) {
	epID, ok := ac.forwardEndpoint(fr.BindAddr, fr.BindPort)
	if !ok {
		return
	}
	if s.ports != nil {
		s.ports.Close(epID)
	}
	s.conns.UnbindEndpoint(epID)
	_ = s.data.DeleteEndpoint(ctx, epID)
	ac.dropForward(epID)
}

func (s *Server) cleanup(ctx context.Context, agentID string) {
	eps, err := s.data.ListEndpointsByAgent(ctx, agentID)
	if err != nil {
		return
	}
	for _, ep := range eps {
		if ep.Kind == store.KindTCP && s.ports != nil {
			s.ports.Close(ep.ID)
		}
		s.conns.UnbindEndpoint(ep.ID)
		if ep.Lifecycle == store.LifecycleEphemeral {
			_ = s.data.DeleteEndpoint(ctx, ep.ID)
		}
	}
}

func (s *Server) allocSubdomain(ctx context.Context, ag *store.Agent, user string) string {
	sub := sanitizeSubdomain(user)
	if sub == "" {
		return store.NewID("")
	}
	if existing, err := s.data.GetEndpointBySubdomain(ctx, sub); err == nil && existing.AgentID != ag.ID {
		return store.NewID("")
	}
	return sub
}

func (s *Server) quotaAllowsEndpoint(ctx context.Context, orgID string) bool {
	q, err := s.data.GetQuota(ctx, orgID)
	if err != nil || q.MaxEndpoints <= 0 {
		return true
	}
	n, err := s.data.CountEndpoints(ctx, orgID)
	if err != nil {
		return true
	}
	return n < q.MaxEndpoints
}

func (s *Server) publicURL(ep *store.Endpoint) string {
	if ep.Kind == store.KindTCP {
		return fmt.Sprintf("tcp://%s:%d", hostOnly(s.baseDomain), ep.Port)
	}
	if ep.Subdomain != "" {
		return fmt.Sprintf("%s://%s.%s", s.publicScheme, ep.Subdomain, s.baseDomain)
	}
	return fmt.Sprintf("%s://%s/tunnel/%s", s.publicScheme, s.baseDomain, ep.ID)
}

func isHTTPPort(p uint32) bool { return p == 80 || p == 8080 || p == 443 }

func sanitizeSubdomain(user string) string {
	user = strings.ToLower(strings.TrimSpace(user))
	switch user {
	case "", "tunnel", "default", "mishmesh", "root", "git", "ssh":
		return ""
	}
	var b strings.Builder
	for _, r := range user {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}
