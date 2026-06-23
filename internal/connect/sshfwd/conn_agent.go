package sshfwd

import (
	"context"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/mishmesh/mishmesh/internal/store"
)

type forwardSpec struct {
	bindAddr string
	bindPort int
}

type forwardedTCPIP struct {
	ConnAddr   string
	ConnPort   uint32
	OriginAddr string
	OriginPort uint32
}

type agentConn struct {
	agentID  string
	user     string
	conn     *ssh.ServerConn
	metrics  Metrics
	mu       sync.Mutex
	forwards map[string]forwardSpec
}

var _ store.AgentConn = (*agentConn)(nil)

func newAgentConn(agentID string, conn *ssh.ServerConn, metrics Metrics) *agentConn {
	return &agentConn{
		agentID:  agentID,
		user:     conn.User(),
		conn:     conn,
		metrics:  metrics,
		forwards: make(map[string]forwardSpec),
	}
}

func (a *agentConn) AgentID() string { return a.agentID }

func (a *agentConn) setForward(endpointID, bindAddr string, bindPort int) {
	a.mu.Lock()
	a.forwards[endpointID] = forwardSpec{bindAddr: bindAddr, bindPort: bindPort}
	a.mu.Unlock()
}

func (a *agentConn) dropForward(endpointID string) {
	a.mu.Lock()
	delete(a.forwards, endpointID)
	a.mu.Unlock()
}

func (a *agentConn) forwardEndpoint(bindAddr string, bindPort uint32) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, spec := range a.forwards {
		if spec.bindAddr == bindAddr && spec.bindPort == int(bindPort) {
			return id, true
		}
	}
	return "", false
}

func (a *agentConn) OpenStream(_ context.Context, endpointID, kind string, _ map[string]string) (net.Conn, error) {
	a.mu.Lock()
	spec, ok := a.forwards[endpointID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("sshfwd: no forward for endpoint %s", endpointID)
	}
	payload := forwardedTCPIP{
		ConnAddr:   spec.bindAddr,
		ConnPort:   uint32(spec.bindPort),
		OriginAddr: "127.0.0.1",
		OriginPort: 1,
	}
	ch, reqs, err := a.conn.OpenChannel("forwarded-tcpip", ssh.Marshal(payload))
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	if a.metrics != nil {
		a.metrics.StreamOpened(kind)
	}
	return newChannelConn(ch, spec.bindAddr, "ssh-origin"), nil
}

func (a *agentConn) Close() error { return a.conn.Close() }
