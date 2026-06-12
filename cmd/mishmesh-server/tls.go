package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"

	"golang.org/x/crypto/acme/autocert"

	"github.com/mishmesh/mishmesh/internal/config"
)

func buildTLSConfig(cfg config.Server) (*tls.Config, http.Handler, error) {
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("load tls keypair: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil, nil
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
		return tc, m.HTTPHandler(nil), nil
	}
	return nil, nil, fmt.Errorf("TLS enabled but no certificate source: set TLS_CERT_FILE/TLS_KEY_FILE or ACME_ENABLED=true")
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
