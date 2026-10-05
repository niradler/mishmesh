package ingress

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	minUsageFlush = 64 << 10
	maxUsageFlush = 1 << 20
)

var errBandwidthExceeded = errors.New("bandwidth quota exceeded")

type meterTarget struct {
	conns store.ConnectionStore
	meter Meter
	orgID string
	kind  string
	limit int64
}

type meteredConn struct {
	net.Conn
	target meterTarget

	in      atomic.Int64
	out     atomic.Int64
	pending atomic.Int64

	flushMu   sync.Mutex
	flushAt   atomic.Int64
	exceeded  atomic.Bool
	closeOnce sync.Once
}

func newMeteredConn(c net.Conn, t meterTarget) *meteredConn {
	m := &meteredConn{Conn: c, target: t}
	m.flushAt.Store(maxUsageFlush)
	if t.limit > 0 && t.orgID != "" {
		m.flushAt.Store(flushThreshold(t.limit - t.conns.Usage(t.orgID)))
	}
	return m
}

func flushThreshold(remaining int64) int64 {
	return min(max(remaining/4, minUsageFlush), maxUsageFlush)
}

func (m *meteredConn) Read(p []byte) (int, error) {
	if m.exceeded.Load() {
		return 0, errBandwidthExceeded
	}
	n, err := m.Conn.Read(p)
	if n > 0 {
		m.out.Add(int64(n))
		m.account(int64(n))
	}
	return n, err
}

func (m *meteredConn) Write(p []byte) (int, error) {
	if m.exceeded.Load() {
		return 0, errBandwidthExceeded
	}
	n, err := m.Conn.Write(p)
	if n > 0 {
		m.in.Add(int64(n))
		m.account(int64(n))
	}
	return n, err
}

func (m *meteredConn) account(n int64) {
	if m.pending.Add(n) >= m.flushAt.Load() {
		m.flush()
	}
}

func (m *meteredConn) flush() {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	pending := m.pending.Swap(0)
	if pending == 0 || m.target.orgID == "" {
		return
	}
	m.target.conns.AddUsage(m.target.orgID, pending)
	if m.target.limit <= 0 {
		return
	}
	remaining := m.target.limit - m.target.conns.Usage(m.target.orgID)
	if remaining <= 0 {
		m.exceeded.Store(true)
		_ = m.Conn.Close()
		return
	}
	m.flushAt.Store(flushThreshold(remaining))
}

func (m *meteredConn) CloseWrite() error { return tunnel.CloseWrite(m.Conn) }

func (m *meteredConn) Close() error {
	err := m.Conn.Close()
	m.closeOnce.Do(func() {
		m.flush()
		if m.target.meter != nil {
			m.target.meter.AddBytes(m.target.kind, m.in.Load(), m.out.Load())
		}
	})
	return err
}
