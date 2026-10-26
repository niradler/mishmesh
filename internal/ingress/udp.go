package ingress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type UDPOptions struct {
	Conns    store.ConnectionStore
	Data     store.DataStore
	Log      *slog.Logger
	BindHost string
	PortMin  int
	PortMax  int
	Meter    Meter
	Idle     time.Duration
	MaxFlows int
}

type UDP struct {
	conns    store.ConnectionStore
	data     store.DataStore
	log      *slog.Logger
	bindHost string
	portMin  int
	portMax  int
	meter    Meter
	idle     time.Duration
	maxFlows int

	mu        sync.Mutex
	listeners map[string]*udpListener
	used      map[int]bool
}

type udpListener struct {
	pc       net.PacketConn
	port     int
	endpoint string
	done     chan struct{}
}

func NewUDP(opts UDPOptions) *UDP {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.BindHost == "" {
		opts.BindHost = "127.0.0.1"
	}
	return &UDP{
		conns:     opts.Conns,
		data:      opts.Data,
		log:       log,
		bindHost:  opts.BindHost,
		portMin:   opts.PortMin,
		portMax:   opts.PortMax,
		meter:     opts.Meter,
		idle:      opts.Idle,
		maxFlows:  opts.MaxFlows,
		listeners: make(map[string]*udpListener),
		used:      make(map[int]bool),
	}
}

func (u *UDP) Open(endpointID string, requestedPort int) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if existing, ok := u.listeners[endpointID]; ok {
		return existing.port, nil
	}
	pc, port, err := u.listen(requestedPort)
	if err != nil {
		return 0, err
	}
	l := &udpListener{pc: pc, port: port, endpoint: endpointID, done: make(chan struct{})}
	u.listeners[endpointID] = l
	u.used[port] = true
	go u.serve(l)
	u.log.Info("udp endpoint listening", "endpoint_id", endpointID, "addr", pc.LocalAddr().String())
	return port, nil
}

func (u *UDP) listen(requestedPort int) (net.PacketConn, int, error) {
	if requestedPort != 0 {
		if u.used[requestedPort] {
			return nil, 0, fmt.Errorf("udp port %d already in use", requestedPort)
		}
		pc, err := net.ListenPacket("udp", net.JoinHostPort(u.bindHost, fmt.Sprint(requestedPort)))
		if err != nil {
			return nil, 0, err
		}
		return pc, requestedPort, nil
	}
	for p := u.portMin; p <= u.portMax; p++ {
		if u.used[p] {
			continue
		}
		pc, err := net.ListenPacket("udp", net.JoinHostPort(u.bindHost, fmt.Sprint(p)))
		if err != nil {
			continue
		}
		return pc, p, nil
	}
	return nil, 0, fmt.Errorf("no free udp port in range %d-%d", u.portMin, u.portMax)
}

func (u *UDP) serve(l *udpListener) {
	defer close(l.done)
	dl := tunnel.NewDatagramListener(l.pc, tunnel.DatagramListenerOptions{
		Open:     func(client net.Addr) (net.Conn, error) { return u.openStream(l.endpoint, client) },
		Idle:     u.idle,
		MaxFlows: u.maxFlows,
		OnFlowDone: func(_ net.Addr, fromClient, toClient int64) {
			u.meterUsage(l.endpoint, fromClient, toClient)
		},
		OnDrop: func(reason string) {
			u.log.Debug("udp datagram dropped", "endpoint_id", l.endpoint, "reason", reason)
		},
	})
	if err := dl.Serve(); err != nil {
		u.log.Warn("udp endpoint stopped", "endpoint_id", l.endpoint, "err", err)
	}
}

var errUDPEndpointOffline = errors.New("udp endpoint has no connected agent")

func (u *UDP) openStream(endpointID string, client net.Addr) (net.Conn, error) {
	agent, ok := u.conns.ResolveEndpoint(endpointID)
	if !ok {
		return nil, errUDPEndpointOffline
	}
	stream, err := agent.OpenStream(context.Background(), endpointID, store.KindUDP, withClientIP(tunnel.WithDatagramIdle(nil, u.idle), peerIP(client)))
	if err != nil {
		u.log.Warn("udp open stream failed", "endpoint_id", endpointID, "err", err)
		return nil, err
	}
	return stream, nil
}

func (u *UDP) meterUsage(endpointID string, up, down int64) {
	if u.data != nil {
		if ep, err := u.data.GetEndpoint(context.Background(), endpointID); err == nil && ep.OrgID != "" {
			u.conns.AddUsage(ep.OrgID, up+down)
		}
	}
	if u.meter != nil {
		u.meter.AddBytes(store.KindUDP, up, down)
	}
}

func (u *UDP) Close(endpointID string) {
	u.mu.Lock()
	l, ok := u.listeners[endpointID]
	if ok {
		delete(u.listeners, endpointID)
		delete(u.used, l.port)
	}
	u.mu.Unlock()
	if !ok {
		return
	}
	_ = l.pc.Close()
	<-l.done
	u.log.Info("udp endpoint closed", "endpoint_id", endpointID, "port", l.port)
}

func (u *UDP) Shutdown() {
	u.mu.Lock()
	ids := make([]string, 0, len(u.listeners))
	for id := range u.listeners {
		ids = append(ids, id)
	}
	u.mu.Unlock()
	for _, id := range ids {
		u.Close(id)
	}
}
