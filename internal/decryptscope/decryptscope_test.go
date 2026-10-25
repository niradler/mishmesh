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

func TestConstraintGroups(t *testing.T) {
	for _, tc := range []struct {
		name          string
		allow, bypass []string
		want          []ConstraintSet
	}{
		{name: "exact excludes its subdomains", allow: []string{"api.example.com"}, want: []ConstraintSet{
			{Permitted: []string{"api.example.com"}, Excluded: []string{".api.example.com"}},
		}},
		{name: "wildcard covers subdomains", allow: []string{"*.corp.example"}, bypass: []string{"bank.corp.example", "*.health.corp.example"}, want: []ConstraintSet{
			{Permitted: []string{".corp.example"}, Excluded: []string{"bank.corp.example", ".health.corp.example"}},
		}},
		{name: "exact with its own wildcard", allow: []string{"*.example.com", "example.com"}, want: []ConstraintSet{
			{Permitted: []string{".example.com", "example.com"}},
		}},
		{name: "exact under a parent wildcard", allow: []string{"*.example.com", "a.example.com"}, want: []ConstraintSet{
			{Permitted: []string{".example.com", "a.example.com"}},
		}},
		{name: "exact with a listed subdomain splits", allow: []string{"a.example.com", "example.com"}, want: []ConstraintSet{
			{Permitted: []string{"example.com"}, Excluded: []string{".example.com"}},
			{Permitted: []string{"a.example.com"}, Excluded: []string{".a.example.com"}},
		}},
		{name: "exact with a listed wildcard below it splits", allow: []string{"*.api.example.com", "example.com", "other.test"}, bypass: []string{"x.api.example.com"}, want: []ConstraintSet{
			{Permitted: []string{"example.com", "other.test"}, Excluded: []string{".example.com", ".other.test", "x.api.example.com"}},
			{Permitted: []string{".api.example.com"}, Excluded: []string{"x.api.example.com"}},
		}},
		{name: "nested exacts", allow: []string{"a.b.example.com", "b.example.com", "example.com"}, want: []ConstraintSet{
			{Permitted: []string{"example.com"}, Excluded: []string{".example.com"}},
			{Permitted: []string{"b.example.com"}, Excluded: []string{".b.example.com"}},
			{Permitted: []string{"a.b.example.com"}, Excluded: []string{".a.b.example.com"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConstraintGroups(tc.allow, tc.bypass)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	if _, err := ConstraintGroups(nil, nil); err == nil {
		t.Fatal("empty allowlist produced constraints")
	}
}

func TestConstraintGroupsAgreeWithDecrypts(t *testing.T) {
	allow := []string{"api.example.com", "*.corp.example", "a.example.org", "example.org", "*.b.example.org", "*.net", "edge.example.net"}
	bypass := []string{"bank.corp.example", "*.health.corp.example"}
	groups, err := ConstraintGroups(allow, bypass)
	if err != nil {
		t.Fatal(err)
	}
	root, rootKey := testCA(t, nil, nil, nil, nil)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	type signer struct {
		cert *x509.Certificate
		key  *ecdsa.PrivateKey
	}
	var inters []signer
	for _, g := range groups {
		cert, key := testCA(t, root, rootKey, g.Permitted, g.Excluded)
		inters = append(inters, signer{cert, key})
	}
	for _, host := range []string{
		"api.example.com", "sub.api.example.com", "example.com", "a.corp.example", "a.b.corp.example", "corp.example",
		"bank.corp.example", "x.bank.corp.example", "health.corp.example", "x.health.corp.example",
		"example.org", "a.example.org", "b.example.org", "x.a.example.org", "c.b.example.org", "x.c.b.example.org",
		"edge.example.net", "x.edge.example.net", "example.net", "other.test",
	} {
		accepted := false
		for _, in := range inters {
			pool := x509.NewCertPool()
			pool.AddCert(in.cert)
			leaf := testLeaf(t, in.cert, in.key, host)
			if _, verr := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: pool, DNSName: host}); verr == nil {
				accepted = true
			}
		}
		if want := Decrypts(allow, bypass, host); accepted != want {
			t.Errorf("%s: chain accepted %v, Decrypts %v", host, accepted, want)
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
