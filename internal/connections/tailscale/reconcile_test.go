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

func TestShouldPreserveForeignServeEntries(t *testing.T) {
	current := map[string]string{"svc:foreign": "http://127.0.0.1:9000", "svc:notes": "http://127.0.0.1:8090"}
	next, err := ReconcileServices(current, map[string]string{"svc:notes": "ep-notes"}, map[string]string{"svc:notes": "http://127.0.0.1:8090", "svc:wiki": "http://127.0.0.1:8090"})
	if err != nil || next["svc:foreign"] != current["svc:foreign"] || next["svc:wiki"] == "" {
		t.Fatalf("foreign entry changed: %v %v", next, err)
	}
}

func TestShouldRejectUnownedDesiredAndRemoveOnlyOwnedEntries(t *testing.T) {
	if _, err := ReconcileServices(map[string]string{"svc:foreign": "x"}, nil, map[string]string{"svc:foreign": "x"}); err == nil {
		t.Fatal("unowned existing entry accepted")
	}
	next, err := ReconcileServices(map[string]string{"svc:old": "x", "svc:foreign": "y"}, map[string]string{"svc:old": "ep-old"}, nil)
	if err != nil || len(next) != 1 || next["svc:foreign"] != "y" {
		t.Fatalf("next=%v err=%v", next, err)
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
