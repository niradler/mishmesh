package ingress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func makeCA(t *testing.T) (caPEM string, signer func(cn string) *x509.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))

	signer = func(cn string) *x509.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		der, _ := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		cert, _ := x509.ParseCertificate(der)
		return cert
	}
	return caPEM, signer
}

func reqWithClientCert(cert *x509.Certificate) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "https://demo.localhost/", nil)
	if cert != nil {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	} else {
		r.TLS = &tls.ConnectionState{}
	}
	return r
}

func TestMTLSGate(t *testing.T) {
	caPEM, sign := makeCA(t)
	ep := &store.Endpoint{Policy: &store.EndpointPolicy{MTLS: &store.MTLSConfig{ClientCAPEM: caPEM}}}

	if !applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(sign("alice")), ep, nil) {
		t.Fatal("valid client cert should pass")
	}
	if applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(nil), ep, nil) {
		t.Fatal("missing client cert should be rejected")
	}

	other, _ := makeCA(t)
	_ = other
	epCN := &store.Endpoint{Policy: &store.EndpointPolicy{MTLS: &store.MTLSConfig{ClientCAPEM: caPEM, AllowedCNs: []string{"bob"}}}}
	if applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(sign("alice")), epCN, nil) {
		t.Fatal("cert CN not in allow-list should be rejected")
	}
	if !applyPolicyGate(httptest.NewRecorder(), reqWithClientCert(sign("bob")), epCN, nil) {
		t.Fatal("cert CN in allow-list should pass")
	}
}
