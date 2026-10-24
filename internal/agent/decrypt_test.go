package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type decryptChainSpec struct {
	interKey   *ecdsa.PrivateKey
	permitted  []string
	isCA       bool
	notAfter   time.Time
	allow      []string
	bypass     []string
	dropRoot   bool
	rogueIssue bool
}

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustCert(t *testing.T, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decryptCertResponse(t *testing.T, now time.Time, spec decryptChainSpec) tunnel.CertResponse {
	t.Helper()
	rootKey := mustKey(t)
	rootTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "org root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	root := mustCert(t, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	signer, parent := rootKey, root
	if spec.rogueIssue {
		rogueKey := mustKey(t)
		rogueTmpl := *rootTmpl
		parent = mustCert(t, &rogueTmpl, &rogueTmpl, &rogueKey.PublicKey, rogueKey)
		signer = rogueKey
	}
	inter := mustCert(t, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "agent intermediate"},
		NotBefore: now.Add(-time.Minute), NotAfter: spec.notAfter,
		IsCA: spec.isCA, BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: spec.isCA,
		KeyUsage: x509.KeyUsageCertSign, PermittedDNSDomainsCritical: true, PermittedDNSDomains: spec.permitted,
	}, parent, &spec.interKey.PublicKey, signer)
	chain := [][]byte{inter.Raw, root.Raw}
	if spec.dropRoot {
		chain = chain[:1]
	}
	return tunnel.CertResponse{Chain: chain, Allow: spec.allow, Bypass: spec.bypass}
}

func TestDecryptStateFromValidatesChain(t *testing.T) {
	now := time.Now()
	d := newDecryptor(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	key := mustKey(t)
	good := decryptChainSpec{interKey: key, permitted: []string{"decrypt.test"}, isCA: true, notAfter: now.Add(24 * time.Hour), allow: []string{"*.decrypt.test"}}

	cases := []struct {
		name   string
		mutate func(*decryptChainSpec)
	}{
		{"chain without a root", func(s *decryptChainSpec) { s.dropRoot = true }},
		{"intermediate for another key", func(s *decryptChainSpec) { s.interKey = mustKey(t) }},
		{"intermediate that is not a CA", func(s *decryptChainSpec) { s.isCA = false }},
		{"intermediate signed by another root", func(s *decryptChainSpec) { s.rogueIssue = true }},
		{"expired intermediate", func(s *decryptChainSpec) { s.notAfter = now.Add(-time.Second) }},
		{"empty allow list", func(s *decryptChainSpec) { s.allow = nil }},
		{"invalid allow entry", func(s *decryptChainSpec) { s.allow = []string{"*"} }},
		{"invalid bypass entry", func(s *decryptChainSpec) { s.bypass = []string{"bad host"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := good
			tc.mutate(&spec)
			if _, err := d.stateFrom(key, decryptCertResponse(t, now, spec)); !errors.Is(err, errDecryptChain) {
				t.Fatalf("stateFrom = %v, want errDecryptChain", err)
			}
		})
	}

	st, err := d.stateFrom(key, decryptCertResponse(t, now, good))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := st.leaf("api.decrypt.test", now)
	if err != nil {
		t.Fatalf("leaf inside the name constraints: %v", err)
	}
	if again, _ := st.leaf("api.decrypt.test", now); again != leaf {
		t.Fatal("leaf was not cached")
	}
	if got := leaf.Leaf.NotAfter; !got.Equal(st.intermediate.NotAfter) {
		t.Fatalf("leaf NotAfter %v outlives or undercuts the intermediate %v", got, st.intermediate.NotAfter)
	}
	if _, err := st.leaf("evil.example", now); !errors.Is(err, errDecryptLeaf) {
		t.Fatalf("leaf outside the name constraints = %v, want errDecryptLeaf", err)
	}
	if _, ok := st.leaves["evil.example"]; ok {
		t.Fatal("an unverifiable leaf was cached")
	}
}

func TestDecryptStateLeafCacheIsBounded(t *testing.T) {
	now := time.Now()
	d := newDecryptor(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	key := mustKey(t)
	st, err := d.stateFrom(key, decryptCertResponse(t, now, decryptChainSpec{
		interKey: key, permitted: []string{"decrypt.test"}, isCA: true, notAfter: now.Add(time.Hour), allow: []string{"*.decrypt.test"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxDecryptLeaves + 5 {
		if _, err := st.leaf(big.NewInt(int64(i)).String()+".decrypt.test", now); err != nil {
			t.Fatal(err)
		}
	}
	if len(st.leaves) != maxDecryptLeaves || len(st.order) != maxDecryptLeaves {
		t.Fatalf("cache holds %d leaves / %d order entries, want %d", len(st.leaves), len(st.order), maxDecryptLeaves)
	}
	if _, ok := st.leaves["0.decrypt.test"]; ok {
		t.Fatal("oldest leaf was not evicted")
	}
}

func TestDecryptStateUsableHonorsExpiryMargin(t *testing.T) {
	now := time.Now()
	st := &decryptState{intermediate: &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(decryptExpiryMargin / 2)}}
	if st.usable(now) {
		t.Fatal("an intermediate inside the expiry margin is usable")
	}
	st.intermediate.NotAfter = now.Add(time.Hour)
	if !st.usable(now) || st.usable(now.Add(-2*time.Hour)) {
		t.Fatal("usable ignores the validity window")
	}
}
