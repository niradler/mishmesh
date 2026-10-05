package ingress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type PortClaims interface {
	Claim(ctx context.Context, endpointID string, requested int) (int, error)
	Release(ctx context.Context, endpointID string) error
	Lookup(ctx context.Context, port int) (endpointID string, ok bool)
}

const claimOpTimeout = 2 * time.Second

type TCPOptions struct {
	Claims   PortClaims
	Conns    store.ConnectionStore
	Data     store.DataStore
	Log      *slog.Logger
	BindHost string
	PortMin  int
	PortMax  int
	Meter    Meter
}

type TCP struct {
	claims     PortClaims
	clusterLns []net.Listener
	conns      store.ConnectionStore
	data       store.DataStore
	log        *slog.Logger
	bindHost   string
	portMin    int
	portMax    int
	meter      Meter

	mu        sync.Mutex
	listeners map[string]*tcpListener
	used      map[int]bool
}

type tcpListener struct {
	ln       net.Listener
	port     int
	endpoint string
}

func NewTCP(opts TCPOptions) *TCP {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.BindHost == "" {
		opts.BindHost = "127.0.0.1"
	}
	return &TCP{
		claims:    opts.Claims,
		conns:     opts.Conns,
		data:      opts.Data,
		log:       log,
		bindHost:  opts.BindHost,
		portMin:   opts.PortMin,
		portMax:   opts.PortMax,
		meter:     opts.Meter,
		listeners: make(map[string]*tcpListener),
		used:      make(map[int]bool),
	}
}

func (t *TCP) Open(endpointID string, requestedPort int) (int, error) {
	if t.claims != nil {
		ctx, cancel := context.WithTimeout(context.Background(), claimOpTimeout)
		defer cancel()
		return t.claims.Claim(ctx, endpointID, requestedPort)
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if existing, ok := t.listeners[endpointID]; ok {
		return existing.port, nil
	}

	ln, port, err := t.listen(requestedPort)
	if err != nil {
		return 0, err
	}
	l := &tcpListener{ln: ln, port: port, endpoint: endpointID}
	t.listeners[endpointID] = l
	t.used[port] = true
	go t.accept(l)
	t.log.Info("tcp endpoint listening", "endpoint_id", endpointID, "addr", ln.Addr().String())
	return port, nil
}

func (t *TCP) listen(requestedPort int) (net.Listener, int, error) {
	if requestedPort != 0 {
		if t.used[requestedPort] {
			return nil, 0, fmt.Errorf("tcp port %d already in use", requestedPort)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", t.bindHost, requestedPort))
		if err != nil {
			return nil, 0, err
		}
		return ln, requestedPort, nil
	}
	for p := t.portMin; p <= t.portMax; p++ {
		if t.used[p] {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", t.bindHost, p))
		if err != nil {
			continue
		}
		return ln, p, nil
	}
	return nil, 0, fmt.Errorf("no free tcp port in range %d-%d", t.portMin, t.portMax)
}

func (t *TCP) Close(endpointID string) {
	if t.claims != nil {
		ctx, cancel := context.WithTimeout(context.Background(), claimOpTimeout)
		defer cancel()
		if err := t.claims.Release(ctx, endpointID); err != nil {
			t.log.Warn("tcp port release failed", "endpoint_id", endpointID, "err", err)
		}
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	l, ok := t.listeners[endpointID]
	if !ok {
		return
	}
	_ = l.ln.Close()
	delete(t.listeners, endpointID)
	delete(t.used, l.port)
	t.log.Info("tcp endpoint closed", "endpoint_id", endpointID, "port", l.port)
}

func (t *TCP) accept(l *tcpListener) {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		go t.handle(conn, l.endpoint)
	}
}

func (t *TCP) handle(client net.Conn, endpointID string) {
	defer client.Close()
	agent, ok := t.conns.ResolveEndpoint(endpointID)
	if !ok {
		return
	}
	stream, err := agent.OpenStream(context.Background(), endpointID, store.KindTCP, nil)
	if err != nil {
		t.log.Warn("tcp open stream failed", "endpoint_id", endpointID, "err", err)
		return
	}
	defer stream.Close()

	up, down := tunnel.Splice(client, stream)
	t.meterUsage(endpointID, up, down)
}

func (t *TCP) meterUsage(endpointID string, up, down int64) {
	if t.data != nil {
		if ep, err := t.data.GetEndpoint(context.Background(), endpointID); err == nil && ep.OrgID != "" {
			t.conns.AddUsage(ep.OrgID, up+down)
		}
	}
	if t.meter != nil {
		t.meter.AddBytes(store.KindTCP, up, down)
	}
}

func (t *TCP) ListenCluster() error {
	if t.claims == nil {
		return errors.New("tcp: cluster listen requires port claims")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for p := t.portMin; p <= t.portMax; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(t.bindHost, strconv.Itoa(p)))
		if err != nil {
			t.log.Warn("tcp cluster port bind failed", "port", p, "err", err)
			continue
		}
		t.clusterLns = append(t.clusterLns, ln)
		go t.acceptClaimed(ln, p)
	}
	if len(t.clusterLns) == 0 {
		return fmt.Errorf("tcp: no port bound in range %d-%d", t.portMin, t.portMax)
	}
	t.log.Info("tcp cluster ports listening", "bind", t.bindHost, "count", len(t.clusterLns), "range", fmt.Sprintf("%d-%d", t.portMin, t.portMax))
	return nil
}

func (t *TCP) acceptClaimed(ln net.Listener, port int) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), claimOpTimeout)
			endpointID, ok := t.claims.Lookup(ctx, port)
			cancel()
			if !ok {
				_ = conn.Close()
				return
			}
			t.handle(conn, endpointID)
		}()
	}
}

func (t *TCP) Shutdown() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ln := range t.clusterLns {
		_ = ln.Close()
	}
	t.clusterLns = nil
	for id, l := range t.listeners {
		_ = l.ln.Close()
		delete(t.listeners, id)
		delete(t.used, l.port)
	}
}
