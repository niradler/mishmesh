package agent

import (
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestDecisionCacheEvictsOldestWhenFull(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	d := newRemoteDecider(clock.Now)
	answer := tunnel.DecisionAnswer{Allowed: true, TTLSeconds: 600}
	for i := range maxCachedDecisions {
		clock.now = clock.now.Add(time.Millisecond)
		d.remember(0, 0, decisionKey{action: "x", port: i}, answer, nil)
	}
	clock.now = clock.now.Add(time.Millisecond)
	d.remember(0, 0, decisionKey{action: "x", port: maxCachedDecisions}, answer, nil)
	if n := len(d.cache); n != maxCachedDecisions {
		t.Fatalf("cache size = %d, want %d", n, maxCachedDecisions)
	}
	if _, ok := d.cache[decisionKey{action: "x", port: 0}]; ok {
		t.Fatal("oldest entry survived eviction")
	}
	for _, port := range []int{1, maxCachedDecisions / 2, maxCachedDecisions} {
		if _, ok := d.cache[decisionKey{action: "x", port: port}]; !ok {
			t.Fatalf("recent entry %d was evicted", port)
		}
	}
}

func TestCheckRequestScopeIsPerDestination(t *testing.T) {
	h := boundHarness(t)
	hostA := egressRequest{host: "a.example.com", port: 443, method: "GET", path: "/one", tls: true, auth: "tok"}
	hostB := egressRequest{host: "b.example.com", port: 443, method: "GET", path: "/one", tls: true, auth: "tok"}

	res := h.requestAsync(hostA)
	h.answerRequest(t, allowAnswer(tunnel.DecisionScope{}))
	if r := waitRequest(t, res); r.err != nil {
		t.Fatal(r.err)
	}
	res = h.requestAsync(hostB)
	h.answerRequest(t, allowAnswer(tunnel.DecisionScope{Path: true, Method: true}))
	if r := waitRequest(t, res); r.err != nil {
		t.Fatal(r.err)
	}

	otherPathOnA := hostA
	otherPathOnA.path = "/two"
	if r := waitRequest(t, h.requestAsync(otherPathOnA)); r.err != nil {
		t.Fatalf("host A decision was not reused after host B answered with a narrower scope: %v", r.err)
	}
}
