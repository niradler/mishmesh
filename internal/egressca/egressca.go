package egressca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/mishmesh/mishmesh/internal/decryptscope"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	RootValidity         = 10 * 365 * 24 * time.Hour
	IntermediateValidity = 24 * time.Hour
	Backdate             = 5 * time.Minute
	maxCSRBytes          = 4 << 10
	serialBits           = 128
)

var (
	ErrNoBox           = errors.New("egress ca: secret key unavailable")
	ErrDecryptDisabled = errors.New("egress decryption is not enabled for this org")
	ErrInvalidCSR      = errors.New("certificate request must be a signed ECDSA P-256 CSR")
)

type Authority struct {
	data store.DataStore
	box  *secrets.Box
	now  func() time.Time
}

func New(data store.DataStore, box *secrets.Box) *Authority {
	return &Authority{data: data, box: box, now: time.Now}
}

type Root struct {
	Cert   *x509.Certificate
	PEM    []byte
	SHA256 string
}

func (a *Authority) Root(ctx context.Context, orgID string) (*Root, error) {
	rec, err := a.ensure(ctx, orgID)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(rec.CertDER)
	if err != nil {
		return nil, fmt.Errorf("parse org ca: %w", err)
	}
	sum := sha256.Sum256(rec.CertDER)
	return &Root{Cert: cert, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rec.CertDER}), SHA256: hex.EncodeToString(sum[:])}, nil
}

func (a *Authority) ensure(ctx context.Context, orgID string) (*store.OrgCA, error) {
	if a == nil || a.box == nil {
		return nil, ErrNoBox
	}
	rec, err := a.data.GetOrgCA(ctx, orgID)
	if err == nil {
		return rec, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("load org ca: %w", err)
	}
	fresh, err := a.newRoot(orgID)
	if err != nil {
		return nil, err
	}
	rec, err = a.data.EnsureOrgCA(ctx, fresh)
	if err != nil {
		return nil, fmt.Errorf("store org ca: %w", err)
	}
	return rec, nil
}

func (a *Authority) newRoot(orgID string) (*store.OrgCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate org ca key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := a.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mishmesh egress CA", Organization: []string{"mishmesh org " + orgID}},
		NotBefore:             now.Add(-Backdate),
		NotAfter:              now.Add(RootValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("sign org ca: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode org ca key: %w", err)
	}
	sealed, err := a.box.Seal(keyDER, store.OrgCAKeyContext(orgID))
	clear(keyDER)
	if err != nil {
		return nil, fmt.Errorf("seal org ca key: %w", err)
	}
	return &store.OrgCA{OrgID: orgID, CertDER: der, KeyEnc: sealed, CreatedAt: now}, nil
}

func (a *Authority) signer(ctx context.Context, orgID string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	rec, err := a.ensure(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(rec.CertDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse org ca: %w", err)
	}
	keyDER, err := a.box.Open(rec.KeyEnc, store.OrgCAKeyContext(orgID))
	if err != nil {
		return nil, nil, fmt.Errorf("open org ca key: %w", err)
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	clear(keyDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse org ca key: %w", err)
	}
	return cert, key, nil
}

type Issued struct {
	Chain    [][]byte
	Allow    []string
	Bypass   []string
	NotAfter time.Time
}

func (a *Authority) Issue(ctx context.Context, orgID, agentID string, csrDER []byte) (*Issued, error) {
	if a == nil || a.box == nil {
		return nil, ErrNoBox
	}
	pub, err := checkCSR(csrDER)
	if err != nil {
		return nil, err
	}
	cfg, err := a.data.GetOrgDecrypt(ctx, orgID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && len(cfg.Allow) == 0) {
		return nil, ErrDecryptDisabled
	}
	if err != nil {
		return nil, fmt.Errorf("load decrypt config: %w", err)
	}
	permitted, excluded, err := decryptscope.Constraints(cfg.Allow, cfg.Bypass)
	if err != nil {
		return nil, ErrDecryptDisabled
	}
	root, rootKey, err := a.signer(ctx, orgID)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := a.now()
	notAfter := now.Add(IntermediateValidity)
	if root.NotAfter.Before(notAfter) {
		notAfter = root.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "mishmesh egress decrypt " + agentID, Organization: []string{"mishmesh org " + orgID}},
		NotBefore:                   now.Add(-Backdate),
		NotAfter:                    notAfter,
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permitted,
		ExcludedDNSDomains:          excluded,
		ExcludedIPRanges:            allAddresses(),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, pub, rootKey)
	if err != nil {
		return nil, fmt.Errorf("sign decrypt intermediate: %w", err)
	}
	inter, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse decrypt intermediate: %w", err)
	}
	return &Issued{Chain: [][]byte{der, root.Raw}, Allow: cfg.Allow, Bypass: cfg.Bypass, NotAfter: inter.NotAfter}, nil
}

func (a *Authority) IssueDecryptCert(ctx context.Context, agent *store.Agent, csr []byte) (tunnel.CertResponse, error) {
	issued, err := a.Issue(ctx, agent.OrgID, agent.ID, csr)
	switch {
	case errors.Is(err, ErrDecryptDisabled), errors.Is(err, ErrInvalidCSR):
		return tunnel.CertResponse{Error: err.Error()}, nil
	case err != nil:
		return tunnel.CertResponse{}, err
	}
	return tunnel.CertResponse{Chain: issued.Chain, Allow: issued.Allow, Bypass: issued.Bypass, NotAfter: issued.NotAfter}, nil
}

func checkCSR(der []byte) (*ecdsa.PublicKey, error) {
	if len(der) == 0 || len(der) > maxCSRBytes {
		return nil, ErrInvalidCSR
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil {
		return nil, ErrInvalidCSR
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, ErrInvalidCSR
	}
	return pub, nil
}

func allAddresses() []*net.IPNet {
	return []*net.IPNet{
		{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
		{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
	}
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), serialBits))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial.Add(serial, big.NewInt(1)), nil
}
