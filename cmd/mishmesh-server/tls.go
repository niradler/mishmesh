package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/mishmesh/mishmesh/internal/config"
)

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
