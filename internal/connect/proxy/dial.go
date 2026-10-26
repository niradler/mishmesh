package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const pinLength = sha256.Size

const (
	maxCachedTLSConfigs = 512
	tlsSessionCacheSize = 32
)

func ValidateOriginTLS(otls *store.OriginTLS) error {
	_, err := originTLSConfig("", otls)
	return err
}

func decodePin(pin string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(pin)
	if err != nil {
		return nil, fmt.Errorf("origin_tls.pin_sha256 %q is not base64: %w", pin, err)
	}
	if len(raw) != pinLength {
		return nil, fmt.Errorf("origin_tls.pin_sha256 %q must decode to %d bytes", pin, pinLength)
	}
	return raw, nil
}

func originTLSKey(host string, otls *store.OriginTLS) string {
	if otls == nil {
		return host
	}
	var b strings.Builder
	b.WriteString(host)
	b.WriteByte(0)
	b.WriteString(otls.ServerName)
	b.WriteByte(0)
	b.WriteString(strconv.FormatBool(otls.InsecureSkipVerify))
	b.WriteByte(0)
	b.WriteString(otls.CAPEM)
	for _, pin := range otls.PinSHA256 {
		b.WriteByte(0)
		b.WriteString(pin)
	}
	return b.String()
}

func (g *Guard) originTLSConfig(host string, otls *store.OriginTLS) (*tls.Config, error) {
	key := originTLSKey(host, otls)
	if cached, ok := g.tlsConfigs.Load(key); ok {
		return cached.(*tls.Config), nil
	}
	cfg, err := originTLSConfig(host, otls)
	if err != nil {
		return nil, err
	}
	cfg.ClientSessionCache = tls.NewLRUClientSessionCache(tlsSessionCacheSize)
	if g.tlsConfigN.Load() < maxCachedTLSConfigs {
		if actual, loaded := g.tlsConfigs.LoadOrStore(key, cfg); loaded {
			return actual.(*tls.Config), nil
		}
		g.tlsConfigN.Add(1)
	}
	return cfg, nil
}

func originTLSConfig(host string, otls *store.OriginTLS) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		ServerName: host,
	}
	if otls == nil {
		return cfg, nil
	}
	if otls.ServerName != "" {
		cfg.ServerName = otls.ServerName
	}
	var pool *x509.CertPool
	if otls.CAPEM != "" {
		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(otls.CAPEM)) {
			return nil, errors.New("origin_tls.ca_pem contains no valid certificate")
		}
	}
	pins := make([][]byte, 0, len(otls.PinSHA256))
	for _, p := range otls.PinSHA256 {
		raw, err := decodePin(p)
		if err != nil {
			return nil, err
		}
		pins = append(pins, raw)
	}
	switch {
	case otls.InsecureSkipVerify:
		cfg.InsecureSkipVerify = true
	case len(pins) > 0:
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("origin presented no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
			for _, pin := range pins {
				if string(pin) == string(sum[:]) {
					return nil
				}
			}
			return errors.New("origin certificate does not match any pin_sha256")
		}
	case pool != nil:
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func (g *Guard) Dial(ctx context.Context, target string, otls *store.OriginTLS, timeout time.Duration) (net.Conn, error) {
	t, err := ParseTarget(target)
	if err != nil {
		return nil, err
	}
	var tlsCfg *tls.Config
	if t.TLS() {
		if tlsCfg, err = g.originTLSConfig(t.Host, otls); err != nil {
			return nil, err
		}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	addrs, err := g.ResolveAll(ctx, target)
	if err != nil {
		return nil, err
	}
	network := "tcp"
	if t.Datagram() {
		network = "udp"
	}
	raw, err := dialFirst(ctx, network, addrs)
	if err != nil {
		return nil, fmt.Errorf("proxy: dial %s: %w", target, err)
	}
	if tlsCfg == nil {
		return raw, nil
	}
	tc := tls.Client(raw, tlsCfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("proxy: tls handshake with %s: %w", target, err)
	}
	return tc, nil
}

func dialFirst(ctx context.Context, network string, addrs []string) (net.Conn, error) {
	var d net.Dialer
	var errs []error
	for _, addr := range addrs {
		c, err := d.DialContext(ctx, network, addr)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
