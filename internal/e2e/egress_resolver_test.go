package e2e

import (
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/connect/proxy/dnstest"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestEgressThroughFilteringResolver(t *testing.T) {
	echo := tcpEchoServer(t)
	_, port, _ := net.SplitHostPort(echo)
	dns := dnstest.Start(t, map[string][]string{
		"allowed.mishmesh.test":   {"127.0.0.1"},
		"filtered.mishmesh.test":  {"0.0.0.0"},
		"filtered6.mishmesh.test": {"::"},
		"rebind.mishmesh.test":    {"127.0.0.1", "169.254.169.254"},
		"private.mishmesh.test":   {"127.0.0.1", "10.0.0.7"},
	})
	permitAll := `permit (principal, action == Action::"egress:connect", resource);`
	forbidLoopback := fmt.Sprintf(`permit (principal, action == Action::"egress:connect", resource);
forbid (principal, action == Action::"egress:connect", resource) when { context has resolved_ip && context.resolved_ip.isLoopback() && resource.port == %s };`, port)
	cases := []struct {
		name     string
		host     string
		policy   string
		want     int
		decision string
		reason   string
	}{
		{"allowed name reaches the backend", "allowed.mishmesh.test", permitAll, http.StatusOK, store.AccessAllow, string(authz.ReasonAllow)},
		{"null ip answer is refused", "filtered.mishmesh.test", permitAll, http.StatusForbidden, store.AccessDeny, "blocked_destination"},
		{"unspecified v6 answer is refused", "filtered6.mishmesh.test", permitAll, http.StatusForbidden, store.AccessDeny, "blocked_destination"},
		{"nxdomain is refused", "missing.mishmesh.test", permitAll, http.StatusBadGateway, store.AccessDeny, "resolve_failed"},
		{"one metadata answer refuses the name", "rebind.mishmesh.test", permitAll, http.StatusForbidden, store.AccessDeny, "blocked_destination"},
		{"one private answer refuses the name", "private.mishmesh.test", permitAll, http.StatusForbidden, store.AccessDeny, "blocked_destination"},
		{"cedar sees the filtered answer", "allowed.mishmesh.test", forbidLoopback, http.StatusForbidden, store.AccessDeny, string(authz.ReasonPolicyDeny)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard, err := proxy.NewGuard(true, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			h := newEgressHarnessWithGuard(t, guard.WithResolver(dns.Resolver()))
			orgID := h.orgWithPolicy(tc.policy)
			listen := h.runAgent(orgID)
			target := net.JoinHostPort(tc.host, port)
			before := dns.Queries(tc.host)
			c, br, status := proxyConnect(t, listen, target)
			if status != tc.want {
				t.Fatalf("CONNECT %s = %d, want %d", target, status, tc.want)
			}
			if status == http.StatusOK {
				assertEcho(t, c, br)
				_ = c.Close()
			}
			if dns.Queries(tc.host) == before {
				t.Fatalf("the filtering resolver was not consulted for %s", tc.host)
			}
			ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == target })
			if ev.Decision != tc.decision || ev.Reason != tc.reason {
				t.Fatalf("access event = %+v, want %s %s", ev, tc.decision, tc.reason)
			}
		})
	}
}
