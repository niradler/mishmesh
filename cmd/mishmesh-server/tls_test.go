package main

import (
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
	p := acmeHostPolicy("mishmesh.io")
	ok := []string{"mishmesh.io", "abc.mishmesh.io", "ABC.MISHMESH.IO"}
	bad := []string{"evil.com", "mishmesh.io.evil.com", "a.b.mishmesh.io.x"}
	for _, h := range ok {
		if err := p(nil, h); err != nil {
			t.Errorf("host %q: want allow, got %v", h, err)
		}
	}
	for _, h := range bad {
		if err := p(nil, h); err == nil {
			t.Errorf("host %q: want reject", h)
		}
	}
}

func TestBuildTLSConfigNoSource(t *testing.T) {
	if _, _, err := buildTLSConfig(config.Server{TLSEnabled: true}); err == nil {
		t.Fatal("expected error when no cert source configured")
	}
}

func TestBuildTLSConfigBYO(t *testing.T) {
	certFile, keyFile := writeSelfSigned(t)
	tc, acmeHTTP, err := buildTLSConfig(config.Server{TLSEnabled: true, TLSCertFile: certFile, TLSKeyFile: keyFile})
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
