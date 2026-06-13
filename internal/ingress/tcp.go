package ingress

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/mishmesh/mishmesh/internal/store"
)

type TCPOptions struct {
	Conns    store.ConnectionStore
	Log      *slog.Logger
	BindHost string
	PortMin  int
	PortMax  int
}

type TCP struct {
	conns    store.ConnectionStore
	log      *slog.Logger
	bindHost string
	portMin  int
	portMax  int

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
		conns:     opts.Conns,
		log:       log,
		bindHost:  opts.BindHost,
		portMin:   opts.PortMin,
		portMax:   opts.PortMax,
		listeners: make(map[string]*tcpListener),
		used:      make(map[int]bool),
	}
}

func (t *TCP) Open(endpointID string, requestedPort int) (int, error) {
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

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, stream); done <- struct{}{} }()
	<-done
}

func (t *TCP) Shutdown() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, l := range t.listeners {
		_ = l.ln.Close()
		delete(t.listeners, id)
		delete(t.used, l.port)
	}
}
