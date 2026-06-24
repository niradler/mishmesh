package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/mishmesh/mishmesh/internal/config"
)

func defaultHostKeyPath(cfg config.Server) string {
	if strings.Contains(cfg.DataDSN, "://") {
		return "ssh_host_ed25519.pem"
	}
	return filepath.Join(filepath.Dir(cfg.DataDSN), "ssh_host_ed25519.pem")
}

func loadOrCreateHostKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ssh host key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal ssh host key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("persist ssh host key %s: %w", path, err)
	}
	return keyPEM, nil
}

func buildTLSConfig(cfg config.Server) (*tls.Config, http.Handler, error) {
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("load tls keypair: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, ClientAuth: tls.RequestClientCert}, nil, nil
	}
	if cfg.ACMEEnabled {
		apex := hostOnly(cfg.BaseDomain)
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(cfg.ACMECacheDir),
			Email:      cfg.ACMEEmail,
			HostPolicy: acmeHostPolicy(apex),
		}
		tc := m.TLSConfig()
		tc.MinVersion = tls.VersionTLS12
		tc.ClientAuth = tls.RequestClientCert
		return tc, m.HTTPHandler(nil), nil
	}
	if cfg.SelfSignedTLS {
		cert, err := selfSignedCert(hostOnly(cfg.BaseDomain))
		if err != nil {
			return nil, nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, ClientAuth: tls.RequestClientCert}, nil, nil
	}
	return nil, nil, fmt.Errorf("TLS enabled but no certificate source: set TLS_CERT_FILE/TLS_KEY_FILE, ACME_ENABLED=true, or SELF_SIGNED_TLS=true")
}

func selfSignedCert(apex string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("self-signed key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("self-signed serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: apex, Organization: []string{"mishmesh"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dedupe([]string{apex, "*." + apex, "localhost"}),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("self-signed cert: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: &tmpl}, nil
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func acmeHostPolicy(apex string) autocert.HostPolicy {
	return func(_ context.Context, host string) error {
		host = strings.ToLower(host)
		if host == apex || strings.HasSuffix(host, "."+apex) {
			return nil
		}
		return fmt.Errorf("acme: host %q not permitted", host)
	}
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}
