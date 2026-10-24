package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/mishmesh/mishmesh/internal/connect/proxy/dnstest"
)

func TestGuardWithResolverChecksFilteredAnswers(t *testing.T) {
	dns := dnstest.Start(t, map[string][]string{
		"public.example.com":   {"93.184.216.34"},
		"public6.example.com":  {"2606:2800:220:1::1"},
		"filtered.example.com": {"0.0.0.0"},
		"rebind.example.com":   {"93.184.216.34", "169.254.169.254"},
		"private.example.com":  {"10.0.0.5"},
		"empty.example.com":    {},
	})
	g := DefaultGuard().WithResolver(dns.Resolver())
	tests := []struct {
		name    string
		host    string
		want    string
		blocked bool
	}{
		{"public answer is dialed", "public.example.com", "93.184.216.34:443", false},
		{"public v6 answer is dialed", "public6.example.com", "[2606:2800:220:1::1]:443", false},
		{"null ip filter answer is blocked", "filtered.example.com", "", true},
		{"any blocked answer blocks", "rebind.example.com", "", true},
		{"private answer is blocked", "private.example.com", "", true},
		{"nxdomain fails resolution", "missing.example.com", "", false},
		{"no answers fails resolution", "empty.example.com", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := g.Resolve(context.Background(), tt.host+":443")
			if dns.Queries(tt.host) == 0 {
				t.Fatalf("resolver for %s was not consulted", tt.host)
			}
			if tt.want != "" {
				if err != nil || addr != tt.want {
					t.Fatalf("Resolve = %q, %v; want %q", addr, err, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("Resolve = %q, want an error", addr)
			}
			if errors.Is(err, ErrBlocked) != tt.blocked {
				t.Fatalf("err = %v, blocked want %v", err, tt.blocked)
			}
		})
	}
}
