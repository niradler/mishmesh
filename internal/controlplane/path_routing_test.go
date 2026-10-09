package controlplane

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestEndpointURLsWithPathRoutingDisabled(t *testing.T) {
	api := New(nil, nil, "", nil)
	api.SetPublicConfig("example.com", "https")
	api.SetPathRouting(false)
	api.SetTLSPublicPort(9443)
	for _, test := range []struct {
		name     string
		endpoint store.Endpoint
		want     string
	}{
		{"path", store.Endpoint{ID: "ep_test", Kind: store.KindHTTP}, ""},
		{"subdomain", store.Endpoint{Subdomain: "shop", Kind: store.KindHTTP}, "https://shop.example.com"},
		{"domain", store.Endpoint{Domain: "shop.example.org", Kind: store.KindHTTP}, "https://shop.example.org"},
		{"tcp", store.Endpoint{Port: 10001, Kind: store.KindTCP}, "tcp://example.com:10001"},
		{"tls", store.Endpoint{Subdomain: "db", Kind: store.KindTLS}, "tls://db.example.com:9443"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := api.toEndpointDTO(&test.endpoint).PublicURL; got != test.want {
				t.Fatalf("public URL = %q, want %q", got, test.want)
			}
		})
	}
	api.SetPathRouting(true)
	if got := api.publicURL(&store.Endpoint{ID: "ep_test"}); got != "https://example.com/tunnel/ep_test" {
		t.Fatalf("enabled path URL = %q", got)
	}
}
