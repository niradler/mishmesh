package sshfwd

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestForwardURLsWithPathRoutingDisabled(t *testing.T) {
	server, err := New(Options{BaseDomain: "example.com", PublicScheme: "https", DisablePathRouting: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.publicURL(&store.Endpoint{ID: "ep_test", Kind: store.KindHTTP}); got != "" {
		t.Fatalf("disabled path URL = %q", got)
	}
	if got := server.publicURL(&store.Endpoint{Subdomain: "shop", Kind: store.KindHTTP}); got != "https://shop.example.com" {
		t.Fatalf("subdomain URL = %q", got)
	}
	if got := server.publicURL(&store.Endpoint{Port: 10001, Kind: store.KindTCP}); got != "tcp://example.com:10001" {
		t.Fatalf("TCP URL = %q", got)
	}
}
