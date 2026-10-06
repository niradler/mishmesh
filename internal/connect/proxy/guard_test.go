package proxy

import (
	"context"
	"net"
	"strings"
	"testing"
)

func mustGuard(t *testing.T, loopback, private bool, cidrs ...string) *Guard {
	t.Helper()
	g, err := NewGuard(loopback, private, cidrs)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGuardValidateTarget(t *testing.T) {
	tests := []struct {
		name   string
		guard  *Guard
		target string
		ok     bool
	}{
		{"public ip", mustGuard(t, false, false), "93.184.216.34:443", true},
		{"public name", mustGuard(t, false, false), "example.com:443", true},
		{"public ipv6", mustGuard(t, false, false), "[2606:4700::1111]:443", true},
		{"rfc1918 10", mustGuard(t, false, false), "10.1.2.3:80", false},
		{"rfc1918 172", mustGuard(t, false, false), "172.20.0.5:80", false},
		{"rfc1918 192", mustGuard(t, false, false), "192.168.1.1:80", false},
		{"cgnat", mustGuard(t, false, false), "100.64.0.1:80", false},
		{"cgnat upper", mustGuard(t, false, false), "100.127.255.254:80", false},
		{"ula", mustGuard(t, false, false), "[fd12:3456::1]:80", false},
		{"ipv4 mapped private", mustGuard(t, false, false), "[::ffff:10.0.0.1]:80", false},
		{"loopback", mustGuard(t, false, false), "127.0.0.1:80", false},
		{"ipv6 loopback", mustGuard(t, false, false), "[::1]:80", false},
		{"metadata", mustGuard(t, true, true), "169.254.169.254:80", false},
		{"metadata ipv6", mustGuard(t, true, true), "[fd00:ec2::254]:80", false},
		{"link local", mustGuard(t, true, true), "169.254.10.10:80", false},
		{"unspecified", mustGuard(t, true, true), "0.0.0.0:80", false},
		{"multicast", mustGuard(t, true, true), "224.0.0.1:80", false},
		{"localhost name", mustGuard(t, false, false), "localhost:80", false},
		{"single label", mustGuard(t, false, false), "redis:6379", false},
		{"svc", mustGuard(t, false, false), "db.prod.svc:5432", false},
		{"cluster local", mustGuard(t, false, false), "db.prod.svc.cluster.local:5432", false},
		{"trailing dot cluster local", mustGuard(t, false, false), "db.prod.svc.cluster.local.:5432", false},
		{"mdns local", mustGuard(t, false, false), "printer.local:80", false},
		{"internal suffix", mustGuard(t, false, false), "metadata.google.internal:80", false},
		{"upper case internal", mustGuard(t, false, false), "DB.SVC:80", false},
		{"bad port", mustGuard(t, false, false), "example.com:0", false},
		{"port out of range", mustGuard(t, false, false), "example.com:70000", false},
		{"no port", mustGuard(t, false, false), "example.com", false},
		{"empty host", mustGuard(t, false, false), ":80", false},
		{"private opt in", mustGuard(t, false, true), "10.1.2.3:80", true},
		{"cgnat opt in", mustGuard(t, false, true), "100.64.0.1:80", true},
		{"service name opt in", mustGuard(t, false, true), "db.prod.svc.cluster.local:5432", true},
		{"single label opt in", mustGuard(t, false, true), "redis:6379", true},
		{"loopback opt in", mustGuard(t, true, false), "127.0.0.1:80", true},
		{"localhost name opt in", mustGuard(t, true, false), "localhost:80", true},
		{"cidr allowlist hit", mustGuard(t, false, false, "10.1.0.0/16"), "10.1.2.3:80", true},
		{"cidr allowlist miss", mustGuard(t, false, false, "10.1.0.0/16"), "10.2.2.3:80", false},
		{"cidr allowlist cgnat", mustGuard(t, false, false, "100.64.0.0/10"), "100.100.1.1:80", true},
		{"cidr allowlist permits internal names", mustGuard(t, false, false, "10.1.0.0/16"), "db.prod.svc:5432", true},
		{"default guard", DefaultGuard(), "10.1.2.3:80", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.guard.ValidateTarget(tc.target)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateTarget(%q) err=%v want ok=%v", tc.target, err, tc.ok)
			}
		})
	}
}

func TestGuardResolveChecksLiteralAndPinsIP(t *testing.T) {
	ctx := context.Background()
	for _, target := range []string{"169.254.169.254:80", "127.0.0.1:80", "0.0.0.0:80", "10.1.2.3:80", "100.64.0.9:80"} {
		if _, err := DefaultGuard().Resolve(ctx, target); err == nil {
			t.Fatalf("expected %q to be blocked", target)
		}
	}
	addr, err := mustGuard(t, false, true).Resolve(ctx, "10.1.2.3:80")
	if err != nil || addr != "10.1.2.3:80" {
		t.Fatalf("private opt-in: addr=%q err=%v", addr, err)
	}
	addr, err = mustGuard(t, true, false).Resolve(ctx, "127.0.0.1:80")
	if err != nil || addr != "127.0.0.1:80" {
		t.Fatalf("loopback opt-in: addr=%q err=%v", addr, err)
	}
	addr, err = mustGuard(t, false, false).Resolve(ctx, "[::ffff:93.184.216.34]:80")
	if err != nil || addr != "93.184.216.34:80" {
		t.Fatalf("mapped public ip should normalize: addr=%q err=%v", addr, err)
	}
}

func TestGuardResolveRejectsInternalNamesBeforeLookup(t *testing.T) {
	_, err := DefaultGuard().Resolve(context.Background(), "db.prod.svc.cluster.local:5432")
	if err == nil || !strings.Contains(err.Error(), "internal name") {
		t.Fatalf("expected internal name rejection, got %v", err)
	}
}

func TestGuardResolveBlocksNameResolvingToPrivate(t *testing.T) {
	g := mustGuard(t, false, false)
	if _, err := g.Resolve(context.Background(), "localtest.me:80"); err == nil {
		if ips, lerr := net.LookupIP("localtest.me"); lerr == nil && len(ips) > 0 {
			t.Fatal("name resolving to loopback must be blocked")
		}
	}
}

func TestNewGuardRejectsBadCIDR(t *testing.T) {
	if _, err := NewGuard(false, false, []string{"not-a-cidr"}); err == nil {
		t.Fatal("expected error for invalid cidr")
	}
}
