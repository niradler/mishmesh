package tailscaleconnection

import (
	"github.com/mishmesh/mishmesh/internal/store"
	"testing"
)

func TestShouldNeverSelectUnsupportedHistoricalAccessProtocols(t *testing.T) {
	for _, kind := range []string{store.KindTCP, store.KindUDP, store.KindTLS} {
		endpoints := []*store.Endpoint{{ID: "unsafe", Kind: kind, Domain: "unsafe.example.org", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "connection", Hostname: "invalid.full.name"}}}}}
		if services, err := desiredServices("connection", endpoints); err != nil || len(services) != 0 {
			t.Fatalf("unsupported %s selected %+v %v", kind, services, err)
		}
	}
}

func TestShouldNormalizeAccessServiceLabels(t *testing.T) {
	for _, input := range []struct{ domain, requested, want string }{
		{"Notes.example.test", "", "notes"}, {"ignored.test", "Wiki", "wiki"},
	} {
		got, err := AccessHostname(input.domain, input.requested)
		if err != nil || got != input.want {
			t.Fatalf("got=%q err=%v", got, err)
		}
	}
	for _, name := range []string{"", "bad.name", "-bad", "bad-", "svc:notes", "bad/name"} {
		if _, err := AccessHostname("", name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}
