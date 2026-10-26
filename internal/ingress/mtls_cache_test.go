package ingress

import (
	"crypto/x509"
	"net/http/httptest"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestMTLSCachedPoolStillEnforcesCA(t *testing.T) {
	caPEM, sign := makeCA(t)
	otherPEM, otherSign := makeCA(t)
	deps := gateDeps{caPools: newTTLCache[*x509.CertPool](clientCAPoolCacheTTL)}
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{MTLS: &store.MTLSConfig{ClientCAPEM: caPEM}}}
	for i := 0; i < 3; i++ {
		if !applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(sign("alice")), ep, deps) {
			t.Fatal("cert from the configured CA should pass")
		}
		if applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(otherSign("mallory")), ep, deps) {
			t.Fatal("cert from a different CA should be rejected")
		}
	}
	rotated := &store.Endpoint{Policy: &store.EndpointPolicy{MTLS: &store.MTLSConfig{ClientCAPEM: otherPEM}}}
	if !applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(otherSign("mallory")), rotated, deps) {
		t.Fatal("rotated CA should be honoured immediately")
	}
	garbage := &store.Endpoint{Policy: &store.EndpointPolicy{MTLS: &store.MTLSConfig{ClientCAPEM: "not a pem"}}}
	if applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(sign("alice")), garbage, deps) {
		t.Fatal("unparseable CA must reject")
	}
}
