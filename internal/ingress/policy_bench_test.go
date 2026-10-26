package ingress

import (
	"crypto/x509"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func BenchmarkCIDRMatch(b *testing.B) {
	cidrs := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "203.0.113.0/24", "198.51.100.7"}
	ip := net.ParseIP("8.8.8.8")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cidrMatch(cidrs, ip)
	}
}

func BenchmarkMTLSGate(b *testing.B) {
	t := &testing.T{}
	caPEM, sign := makeCA(t)
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{MTLS: &store.MTLSConfig{ClientCAPEM: caPEM}}}
	r := reqWithClientCert(sign("alice"))
	deps := gateDeps{caPools: newTTLCache[*x509.CertPool](clientCAPoolCacheTTL)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !applyPolicyGate(httptest.NewRecorder(), r, ep, deps) {
			b.Fatal("rejected")
		}
	}
}
