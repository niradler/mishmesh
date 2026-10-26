package agent

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"sync"
	"time"
)

type decryptSigner struct {
	cert *x509.Certificate
	pool *x509.CertPool
}

type decryptState struct {
	key       *ecdsa.PrivateKey
	leafKey   *ecdsa.PrivateKey
	signers   []decryptSigner
	notBefore time.Time
	notAfter  time.Time
	roots     *x509.CertPool
	allow     []string
	bypass    []string

	mu       sync.Mutex
	leaves   map[string]*tls.Certificate
	order    []string
	inflight map[string]*leafFlight
}

func (s *decryptState) usable(now time.Time) bool {
	if s == nil {
		return false
	}
	return now.Add(decryptExpiryMargin).Before(s.notAfter) && !now.Before(s.notBefore)
}

type leafFlight struct {
	done chan struct{}
	cert *tls.Certificate
	err  error
}

func (s *decryptState) leaf(host string, now time.Time) (*tls.Certificate, error) {
	if s == nil {
		return nil, errDecryptUnavailable
	}
	s.mu.Lock()
	if c, ok := s.leaves[host]; ok && now.Add(decryptExpiryMargin).Before(c.Leaf.NotAfter) {
		s.mu.Unlock()
		return c, nil
	}
	if flight, ok := s.inflight[host]; ok {
		s.mu.Unlock()
		<-flight.done
		return flight.cert, flight.err
	}
	flight := &leafFlight{done: make(chan struct{})}
	if s.inflight == nil {
		s.inflight = map[string]*leafFlight{}
	}
	s.inflight[host] = flight
	s.mu.Unlock()

	flight.cert, flight.err = s.mint(host, now)

	s.mu.Lock()
	delete(s.inflight, host)
	if flight.err == nil {
		s.storeLeafLocked(host, flight.cert)
	}
	s.mu.Unlock()
	close(flight.done)
	return flight.cert, flight.err
}

func (s *decryptState) storeLeafLocked(host string, c *tls.Certificate) {
	if s.leaves == nil {
		s.leaves = map[string]*tls.Certificate{}
	}
	if _, ok := s.leaves[host]; !ok {
		if len(s.order) >= maxDecryptLeaves {
			delete(s.leaves, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, host)
	}
	s.leaves[host] = c
}

func (s *decryptState) mint(host string, now time.Time) (*tls.Certificate, error) {
	err := errDecryptLeaf
	for _, signer := range s.signers {
		var c *tls.Certificate
		if c, err = s.mintWith(signer, host, now); err == nil {
			return c, nil
		}
	}
	return nil, err
}

func (s *decryptState) mintWith(signer decryptSigner, host string, now time.Time) (*tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), decryptSerialBits))
	if err != nil {
		return nil, fmt.Errorf("decrypt leaf serial: %w", err)
	}
	notBefore := now.Add(-decryptLeafBackdate)
	if notBefore.Before(signer.cert.NotBefore) {
		notBefore = signer.cert.NotBefore
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial.Add(serial, big.NewInt(1)),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             notBefore,
		NotAfter:              signer.cert.NotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer.cert, &s.leafKey.PublicKey, s.key)
	if err != nil {
		return nil, fmt.Errorf("decrypt leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("decrypt leaf: %w", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName: host, Roots: s.roots, Intermediates: signer.pool, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, fmt.Errorf("%w: %w", errDecryptLeaf, err)
	}
	return &tls.Certificate{Certificate: [][]byte{der, signer.cert.Raw}, PrivateKey: s.leafKey, Leaf: leaf}, nil
}
