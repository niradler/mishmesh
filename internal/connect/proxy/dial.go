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
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const pinLength = sha256.Size

func ValidateOriginTLS(otls *store.OriginTLS) error {
	if otls == nil {
		return nil
	}
	if otls.CAPEM != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(otls.CAPEM)) {
			return errors.New("origin_tls.ca_pem contains no valid certificate")
		}
	}
	for _, pin := range otls.PinSHA256 {
		if _, err := decodePin(pin); err != nil {
			return err
		}
	}
	return nil
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

func originTLSConfig(host string, otls *store.OriginTLS) (*tls.Config, error) {
	if err := ValidateOriginTLS(otls); err != nil {
		return nil, err
	}
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
	switch {
	case otls.InsecureSkipVerify:
		cfg.InsecureSkipVerify = true
	case len(otls.PinSHA256) > 0:
		pins := make([][]byte, 0, len(otls.PinSHA256))
		for _, p := range otls.PinSHA256 {
			raw, err := decodePin(p)
			if err != nil {
				return nil, err
			}
			pins = append(pins, raw)
		}
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
	case otls.CAPEM != "":
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM([]byte(otls.CAPEM))
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
		if tlsCfg, err = originTLSConfig(t.Host, otls); err != nil {
			return nil, err
		}
	}
	addr, err := g.Resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	network := "tcp"
	if t.Datagram() {
		network = "udp"
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, network, addr)
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
