package endpointurl

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestTLS(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint store.Endpoint
		base     string
		port     int
		want     string
	}{
		{"default port", store.Endpoint{Subdomain: "test"}, "example.com:8080", 0, "tls://test.example.com:8444"},
		{"custom domain", store.Endpoint{Domain: "db.example.net"}, "example.com", 443, "tls://db.example.net:443"},
		{"subdomain service port", store.Endpoint{Subdomain: "test"}, "example.com", 9443, "tls://test.example.com:9443"},
		{"no host", store.Endpoint{}, "example.com", 8444, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := TLS(&test.endpoint, test.base, test.port); got != test.want {
				t.Fatalf("URL = %q, want %q", got, test.want)
			}
		})
	}
}
