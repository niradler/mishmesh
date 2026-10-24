package decryptscope

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	got, err := Normalize([]string{" API.Example.com. ", "*.corp.example", "api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.corp.example", "api.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"", "*", "*.", "10.0.0.1", "[::1]", "::1", "a..b", "-a.com", "a_b.com", "a.*.com", "*.*.com", "a b", strings.Repeat("a", 64) + ".com"} {
		if _, err := Normalize([]string{bad}); err == nil {
			t.Errorf("Normalize(%q) accepted", bad)
		}
	}
	many := make([]string, MaxPatterns+1)
	for i := range many {
		many[i] = fmt.Sprintf("h%d.example", i)
	}
	if _, err := Normalize(many); err == nil {
		t.Fatal("unbounded list accepted")
	}
}

func TestDecrypts(t *testing.T) {
	allow := []string{"api.example.com", "*.corp.example"}
	bypass := []string{"bank.corp.example", "*.health.corp.example"}
	for host, want := range map[string]bool{
		"api.example.com":           true,
		"API.EXAMPLE.COM.":          true,
		"sub.api.example.com":       false,
		"example.com":               false,
		"corp.example":              false,
		"a.corp.example":            true,
		"a.b.corp.example":          true,
		"bank.corp.example":         false,
		"x.bank.corp.example":       false,
		"health.corp.example":       true,
		"x.health.corp.example":     false,
		"evilcorp.example":          false,
		"10.0.0.1":                  false,
		"":                          false,
		"api.example.com.evil.test": false,
	} {
		if got := Decrypts(allow, bypass, host); got != want {
			t.Errorf("Decrypts(%q) = %v, want %v", host, got, want)
		}
	}
	if Decrypts(nil, nil, "api.example.com") {
		t.Fatal("empty allowlist decrypted")
	}
}

func TestConstraints(t *testing.T) {
	for _, tc := range []struct {
		name               string
		allow, bypass      []string
		permitted, exclude []string
	}{
		{name: "exact excludes its subdomains", allow: []string{"api.example.com"}, permitted: []string{"api.example.com"}, exclude: []string{".api.example.com"}},
		{name: "wildcard", allow: []string{"*.corp.example"}, bypass: []string{"bank.corp.example", "*.health.corp.example"}, permitted: []string{".corp.example"}, exclude: []string{"bank.corp.example", ".health.corp.example"}},
		{name: "exact with its own wildcard", allow: []string{"*.example.com", "example.com"}, permitted: []string{".example.com", "example.com"}},
		{name: "exact with a listed subdomain", allow: []string{"a.example.com", "example.com"}, permitted: []string{"a.example.com", "example.com"}, exclude: []string{".a.example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permitted, excluded, err := Constraints(tc.allow, tc.bypass)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(permitted, tc.permitted) || !reflect.DeepEqual(excluded, tc.exclude) {
				t.Fatalf("got permitted %v excluded %v, want %v %v", permitted, excluded, tc.permitted, tc.exclude)
			}
		})
	}
	if _, _, err := Constraints(nil, nil); err == nil {
		t.Fatal("empty allowlist produced constraints")
	}
}

func TestConstraintsAgreeWithDecrypts(t *testing.T) {
	allow := []string{"api.example.com", "*.corp.example", "a.example.org", "example.org"}
	bypass := []string{"bank.corp.example", "*.health.corp.example"}
	permitted, excluded, err := Constraints(allow, bypass)
	if err != nil {
		t.Fatal(err)
	}
	root, rootKey := testCA(t, nil, nil, nil, nil)
	inter, interKey := testCA(t, root, rootKey, permitted, excluded)
	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root)
	inters.AddCert(inter)
	exactParentWithListedChild := map[string]bool{"b.example.org": true, "x.a.example.org": true}
	for _, host := range []string{
		"api.example.com", "sub.api.example.com", "example.com", "a.corp.example", "a.b.corp.example", "corp.example",
		"bank.corp.example", "x.bank.corp.example", "health.corp.example", "x.health.corp.example",
		"example.org", "a.example.org", "b.example.org", "x.a.example.org", "other.test",
	} {
		leaf := testLeaf(t, inter, interKey, host)
		_, verr := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, DNSName: host})
		if Decrypts(allow, bypass, host) && verr != nil {
			t.Errorf("%s: agent would decrypt but the constrained chain rejects it: %v", host, verr)
		}
		if !Decrypts(allow, bypass, host) && verr == nil && !exactParentWithListedChild[host] {
			t.Errorf("%s: outside the scope but the constrained chain accepts it", host)
		}
	}
}

func testCA(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, permitted, excluded []string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		PermittedDNSDomainsCritical: permitted != nil, PermittedDNSDomains: permitted, ExcludedDNSDomains: excluded,
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func testLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, host string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(30 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
