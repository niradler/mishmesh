package egressca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newAuthority(t *testing.T) (*Authority, store.DataStore) {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "ca.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	for _, id := range []string{"org-a", "org-b"} {
		if err := data.CreateOrg(context.Background(), &store.Org{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(data, box), data
}

func csrFor(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "evil"}, DNSNames: []string{"*"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func p256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func enableDecrypt(t *testing.T, data store.DataStore, org string, allow, bypass []string) {
	t.Helper()
	if err := data.SetOrgDecrypt(context.Background(), &store.OrgDecrypt{OrgID: org, Allow: allow, Bypass: bypass, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func leafUnder(t *testing.T, inter *x509.Certificate, key *ecdsa.PrivateKey, dns []string, ips []net.IP) *x509.Certificate {
	t.Helper()
	leafKey := p256(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "leaf"}, DNSNames: dns, IPAddresses: ips,
		NotBefore: time.Now().Add(-time.Minute), NotAfter: inter.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, inter, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func TestRootIsLazyStableAndSealed(t *testing.T) {
	a, data := newAuthority(t)
	ctx := context.Background()
	first, err := a.Root(ctx, "org-a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.Root(ctx, "org-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != again.SHA256 {
		t.Fatal("root changed between calls")
	}
	other, err := a.Root(ctx, "org-b")
	if err != nil {
		t.Fatal(err)
	}
	if other.SHA256 == first.SHA256 {
		t.Fatal("two orgs share a root")
	}
	c := first.Cert
	if !c.IsCA || c.MaxPathLen != 1 || c.KeyUsage&x509.KeyUsageCertSign == 0 || c.NotAfter.Sub(c.NotBefore) < RootValidity {
		t.Fatalf("root = IsCA %v MaxPathLen %d usage %v validity %v", c.IsCA, c.MaxPathLen, c.KeyUsage, c.NotAfter.Sub(c.NotBefore))
	}
	rec, err := data.GetOrgCA(ctx, "org-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseECPrivateKey(rec.KeyEnc); err == nil {
		t.Fatal("org ca key stored in the clear")
	}
	if _, err := a.box.Open(rec.KeyEnc, store.OrgCAKeyContext("org-b")); err == nil {
		t.Fatal("org ca key opens under another org's context")
	}
}

func TestRootWithoutBoxFailsClosed(t *testing.T) {
	a, data := newAuthority(t)
	a.box = nil
	if _, err := a.Root(context.Background(), "org-a"); !errors.Is(err, ErrNoBox) {
		t.Fatalf("err = %v, want ErrNoBox", err)
	}
	if _, err := a.Issue(context.Background(), "org-a", "ag", csrFor(t, p256(t))); !errors.Is(err, ErrNoBox) {
		t.Fatalf("issue err = %v, want ErrNoBox", err)
	}
	if _, err := data.GetOrgCA(context.Background(), "org-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a root was created without a box")
	}
}

func TestIssueRefusesWithoutAllowlist(t *testing.T) {
	a, data := newAuthority(t)
	if _, err := a.Issue(context.Background(), "org-a", "ag", csrFor(t, p256(t))); !errors.Is(err, ErrDecryptDisabled) {
		t.Fatalf("err = %v, want ErrDecryptDisabled", err)
	}
	enableDecrypt(t, data, "org-a", nil, []string{"x.example"})
	if _, err := a.Issue(context.Background(), "org-a", "ag", csrFor(t, p256(t))); !errors.Is(err, ErrDecryptDisabled) {
		t.Fatalf("bypass-only err = %v, want ErrDecryptDisabled", err)
	}
}

func TestIssueRejectsBadRequests(t *testing.T) {
	a, data := newAuthority(t)
	enableDecrypt(t, data, "org-a", []string{"api.example.com"}, nil)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tampered := csrFor(t, p256(t))
	tampered[len(tampered)-3] ^= 0xff
	for name, csr := range map[string][]byte{
		"empty":     nil,
		"garbage":   []byte("not a csr"),
		"rsa":       csrFor(t, rsaKey),
		"p384":      csrFor(t, p384),
		"bad sig":   tampered,
		"oversized": make([]byte, maxCSRBytes+1),
	} {
		if _, err := a.Issue(context.Background(), "org-a", "ag", csr); !errors.Is(err, ErrInvalidCSR) {
			t.Errorf("%s: err = %v, want ErrInvalidCSR", name, err)
		}
	}
}

func TestIssuedIntermediateIsConstrainedByTheServer(t *testing.T) {
	a, data := newAuthority(t)
	ctx := context.Background()
	enableDecrypt(t, data, "org-a", []string{"*.corp.example", "api.example.com"}, []string{"bank.corp.example"})
	key := p256(t)
	issued, err := a.Issue(ctx, "org-a", "agent-1", csrFor(t, key))
	if err != nil {
		t.Fatal(err)
	}
	if len(issued.Chain) != 2 {
		t.Fatalf("chain length = %d", len(issued.Chain))
	}
	inter, err := x509.ParseCertificate(issued.Chain[0])
	if err != nil {
		t.Fatal(err)
	}
	root, err := a.Root(ctx, "org-a")
	if err != nil {
		t.Fatal(err)
	}
	if string(issued.Chain[1]) != string(root.Cert.Raw) {
		t.Fatal("chain does not end in the org root")
	}
	if !inter.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatal("intermediate is not bound to the CSR key")
	}
	if !inter.IsCA || !inter.MaxPathLenZero || inter.KeyUsage != x509.KeyUsageCertSign || !inter.PermittedDNSDomainsCritical {
		t.Fatalf("intermediate basics: IsCA %v MaxPathLenZero %v usage %v critical %v", inter.IsCA, inter.MaxPathLenZero, inter.KeyUsage, inter.PermittedDNSDomainsCritical)
	}
	if len(inter.ExcludedIPRanges) != 2 || len(inter.DNSNames) != 0 || inter.Subject.CommonName == "evil" {
		t.Fatalf("intermediate took input from the CSR or misses IP exclusions: %+v", inter)
	}
	if life := inter.NotAfter.Sub(inter.NotBefore); life > IntermediateValidity+Backdate {
		t.Fatalf("intermediate lifetime %v", life)
	}
	if !issued.NotAfter.Equal(inter.NotAfter) {
		t.Fatalf("reported NotAfter %v != cert %v", issued.NotAfter, inter.NotAfter)
	}
	if inter.SerialNumber.BitLen() < 64 {
		t.Fatalf("serial %v is not random enough", inter.SerialNumber)
	}

	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root.Cert)
	inters.AddCert(inter)
	verify := func(leaf *x509.Certificate, name string) error {
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, DNSName: name})
		return err
	}
	for host, ok := range map[string]bool{
		"api.example.com":     true,
		"a.corp.example":      true,
		"bank.corp.example":   false,
		"sub.api.example.com": false,
		"evil.test":           false,
	} {
		err := verify(leafUnder(t, inter, key, []string{host}, nil), host)
		if (err == nil) != ok {
			t.Errorf("%s: verify err = %v, want ok=%v", host, err, ok)
		}
	}
	if err := verify(leafUnder(t, inter, key, []string{"a.corp.example"}, []net.IP{net.ParseIP("10.0.0.1")}), "a.corp.example"); err == nil {
		t.Error("leaf carrying an IP SAN verified")
	}

	otherRoot, err := a.Root(ctx, "org-b")
	if err != nil {
		t.Fatal(err)
	}
	foreign := x509.NewCertPool()
	foreign.AddCert(otherRoot.Cert)
	leaf := leafUnder(t, inter, key, []string{"api.example.com"}, nil)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: foreign, Intermediates: inters, DNSName: "api.example.com"}); err == nil {
		t.Fatal("org-a intermediate verified against org-b root")
	}
}

func TestIssueDecryptCertMapsPublicErrors(t *testing.T) {
	a, _ := newAuthority(t)
	resp, err := a.IssueDecryptCert(context.Background(), &store.Agent{ID: "ag", OrgID: "org-a"}, csrFor(t, p256(t)))
	if err != nil || resp.Error != ErrDecryptDisabled.Error() || len(resp.Chain) != 0 {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
}
