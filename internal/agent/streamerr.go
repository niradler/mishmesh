package agent

import (
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func reportStreamFailure(stream net.Conn, init tunnel.StreamInit, reason string) {
	if init.Kind != store.KindHTTP {
		return
	}
	_ = tunnel.WriteStreamError(stream, reason)
}

func notServedReason(init tunnel.StreamInit) string {
	if init.EndpointID == "" {
		return "target not permitted by the agent allowlist"
	}
	return "endpoint is not served by this agent"
}

func dialFailureReason(err error) string {
	var ne net.Error
	var dns *net.DNSError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "refused"):
		return "connection refused"
	case errors.As(err, &dns):
		return "host not found"
	case errors.As(err, &ne) && ne.Timeout():
		return "connection timed out"
	case strings.Contains(err.Error(), "local tls handshake"):
		return "TLS handshake with the local service failed"
	default:
		return "connection failed"
	}
}
