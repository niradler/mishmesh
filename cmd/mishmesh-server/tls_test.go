package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/config"
)

func TestAcmeHostPolicy(t *testing.T) {
	subs := map[string]bool{"abc": true}
	customs := map[string]bool{"app.customer.com": true}
	lookup := func(m map[string]bool) hostLookup {
		return func(_ context.Context, h string) (bool, error) { return m[h], nil }
	}
	p := acmeHostPolicy("mishmesh.io", lookup(subs), lookup(customs), "api.mishmesh.io")
	tests := []struct {
		host string
		want bool
	}{
		{"mishmesh.io", true},
		{"MISHMESH.IO", true},
		{"abc.mishmesh.io", true},
		{"ABC.MISHMESH.IO", true},
		{"api.mishmesh.io", true},
		{"random1.mishmesh.io", false},
		{"a.b.mishmesh.io", false},
		{"x.abc.mishmesh.io", false},
		{".mishmesh.io", false},
		{"app.customer.com", true},
		{"evil.com", false},
		{"mishmesh.io.evil.com", false},
		{"a.b.mishmesh.io.x", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			err := p(context.Background(), tt.host)
			if (err == nil) != tt.want {
				t.Fatalf("host %q: want allow=%v, got err=%v", tt.host, tt.want, err)
			}
		})
	}
}

func TestAcmeHostPolicyNoStoreRejectsSubdomains(t *testing.T) {
	p := acmeHostPolicy("mishmesh.io", nil, nil)
	if err := p(context.Background(), "abc.mishmesh.io"); err == nil {
		t.Fatal("subdomain must be rejected without a store")
	}
	if err := p(context.Background(), "mishmesh.io"); err != nil {
		t.Fatalf("apex must be allowed: %v", err)
	}
}

func TestBuildTLSConfigNoSource(t *testing.T) {
	if _, _, err := buildTLSConfig(config.Server{TLSEnabled: true}, nil); err == nil {
		t.Fatal("expected error when no cert source configured")
	}
}

func TestBuildTLSConfigBYO(t *testing.T) {
	certFile, keyFile := writeSelfSigned(t)
	tc, acmeHTTP, err := buildTLSConfig(config.Server{TLSEnabled: true, TLSCertFile: certFile, TLSKeyFile: keyFile}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if acmeHTTP != nil {
		t.Fatal("BYO mode should not return an ACME handler")
	}
	if len(tc.Certificates) != 1 {
		t.Fatalf("want 1 certificate, got %d", len(tc.Certificates))
	}
}

func writeSelfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.test.local"},
		DNSNames:     []string{"*.test.local", "test.local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certOut, _ := os.Create(certFile)
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	_ = certOut.Close()

	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyOut, _ := os.Create(keyFile)
	_ = pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	_ = keyOut.Close()
	return certFile, keyFile
}
