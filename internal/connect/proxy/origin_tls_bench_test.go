package proxy

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func benchOriginPolicies(b *testing.B) map[string]*store.OriginTLS {
	b.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	b.Cleanup(srv.Close)
	cert := srv.Certificate()
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return map[string]*store.OriginTLS{
		"ca":  {CAPEM: certPEM(cert.Raw)},
		"pin": {PinSHA256: []string{base64.StdEncoding.EncodeToString(sum[:])}},
	}
}

func BenchmarkOriginTLSConfig(b *testing.B) {
	for name, otls := range benchOriginPolicies(b) {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := originTLSConfig("origin.example.com", otls); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGuardOriginTLSConfigCached(b *testing.B) {
	g := &Guard{}
	for name, otls := range benchOriginPolicies(b) {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := g.originTLSConfig("origin.example.com", otls); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestOriginTLSConfigCachedPerPolicy(t *testing.T) {
	g := &Guard{}
	first, err := g.originTLSConfig("h", &store.OriginTLS{ServerName: "a.test"})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := g.originTLSConfig("h", &store.OriginTLS{ServerName: "a.test"})
	if first != again {
		t.Fatal("equal policy built a second config")
	}
	other, _ := g.originTLSConfig("h", &store.OriginTLS{ServerName: "b.test"})
	if other == first || other.ServerName != "b.test" {
		t.Fatal("different policy shared a config")
	}
	if _, err := g.originTLSConfig("h", &store.OriginTLS{CAPEM: "junk"}); err == nil {
		t.Fatal("invalid CA accepted")
	}
	if first.ClientSessionCache == nil {
		t.Fatal("no session cache for resumption")
	}
}
