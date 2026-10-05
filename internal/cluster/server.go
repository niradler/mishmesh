package cluster

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	headerReadTimeout = 10 * time.Second
	openStreamTimeout = 10 * time.Second
)

type LocalResolver interface {
	LocalAgent(agentID string) (store.AgentConn, bool)
}

type ServerOptions struct {
	Secret []byte
	Local  LocalResolver
	Log    *slog.Logger
	Now    func() time.Time
}

type Server struct {
	secret []byte
	local  LocalResolver
	log    *slog.Logger
	now    func() time.Time

	mu     sync.Mutex
	ln     net.Listener
	active map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

func NewServer(opts ServerOptions) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		secret: opts.Secret,
		local:  opts.Local,
		log:    log,
		now:    now,
		active: make(map[net.Conn]struct{}),
	}
}

func (s *Server) Listen(addr string) (net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.wg.Add(1)
	go s.acceptLoop(ln)
	return ln.Addr(), nil
}

func (s *Server) acceptLoop(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if !s.track(conn) {
			_ = conn.Close()
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(conn)
		}()
	}
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.active[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.active, c)
	s.mu.Unlock()
	_ = c.Close()
}

func (s *Server) Shutdown() {
	s.mu.Lock()
	s.closed = true
	ln := s.ln
	conns := make([]net.Conn, 0, len(s.active))
	for c := range s.active {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
}

func (s *Server) handle(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(headerReadTimeout))
	hdr, err := ReadHeader(conn)
	if err != nil {
		s.log.Warn("relay header rejected", "remote", conn.RemoteAddr().String(), "err", err)
		return
	}
	if err := hdr.Verify(s.secret, s.now()); err != nil {
		s.log.Warn("relay auth failed", "remote", conn.RemoteAddr().String(), "err", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	agent, ok := s.local.LocalAgent(hdr.AgentID)
	if !ok {
		writeStatus(conn, StatusNotHere)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), openStreamTimeout)
	stream, err := agent.OpenStream(ctx, hdr.EndpointID, hdr.Kind, hdr.Meta)
	cancel()
	if err != nil {
		s.log.Warn("relay open stream failed", "agent_id", hdr.AgentID, "endpoint_id", hdr.EndpointID, "err", err)
		writeStatus(conn, StatusError)
		return
	}
	defer stream.Close()
	if !writeStatus(conn, StatusOK) {
		return
	}
	splice(conn, stream)
}

func writeStatus(w io.Writer, status byte) bool {
	_, err := w.Write([]byte{status})
	return err == nil
}

func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}
