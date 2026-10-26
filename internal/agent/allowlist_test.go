package agent

import (
	"context"
	"net"
	"testing"
)

func TestAllowlistDenyFirst(t *testing.T) {
	a := NewAllowlist(nil)
	if a.Allowed("93.184.216.34:80") {
		t.Fatal("empty allowlist must deny everything")
	}
}

func TestAllowlistRules(t *testing.T) {
	a := NewAllowlist([]string{"10.0.0.0/8:80;443", "93.184.216.34"})
	cases := []struct {
		target string
		want   bool
	}{
		{"10.1.2.3:80", true},
		{"10.1.2.3:443", true},
		{"10.1.2.3:22", false},
		{"11.0.0.1:80", false},
		{"93.184.216.34:8080", true},
		{"127.0.0.1:80", false},
		{"169.254.169.254:80", false},
	}
	for _, c := range cases {
		if got := a.Allowed(c.target); got != c.want {
			t.Errorf("Allowed(%q)=%v want %v", c.target, got, c.want)
		}
	}
}

func TestAllowlistResolvePinsIP(t *testing.T) {
	a := NewAllowlist([]string{"8.8.8.8:53"})
	dialAddr, serverName, ok := a.Resolve(context.Background(), "8.8.8.8:53")
	if !ok || dialAddr != "8.8.8.8:53" || serverName != "8.8.8.8" {
		t.Fatalf("Resolve pinning: addr=%q sni=%q ok=%v", dialAddr, serverName, ok)
	}
	if _, _, ok := a.Resolve(context.Background(), "169.254.169.254:53"); ok {
		t.Fatal("metadata target must not resolve")
	}
}

func TestAllowlistHardDenyOverridesBroadRule(t *testing.T) {
	a := NewAllowlist([]string{"0.0.0.0/0"})
	if a.Allowed("169.254.169.254:80") {
		t.Fatal("metadata IP must be hard-denied even under 0.0.0.0/0")
	}
	if a.Allowed("127.0.0.1:8080") {
		t.Fatal("loopback must be hard-denied even under 0.0.0.0/0")
	}
	if !a.Allowed("8.8.8.8:53") {
		t.Fatal("public IP under 0.0.0.0/0 should be allowed")
	}
}

func TestAllowlistHardDeniesEmbeddedUnspecifiedAndMulticast(t *testing.T) {
	a := NewAllowlist([]string{"0.0.0.0/0", "::/0"})
	for _, target := range []string{
		"[64:ff9b::a9fe:a9fe]:80",
		"[64:ff9b::7f00:1]:80",
		"0.0.0.1:80",
		"224.0.0.1:80",
		"255.255.255.255:80",
	} {
		if a.Allowed(target) {
			t.Fatalf("%s must be hard-denied", target)
		}
	}
}

func TestAllowlistSkipsLookupForUnmatchableHost(t *testing.T) {
	cases := []struct {
		name       string
		specs      []string
		target     string
		wantLookup bool
		wantOK     bool
	}{
		{"no rules", nil, "secret.internal.example:80", false, false},
		{"host rule for other name", []string{"api.example.com"}, "secret.internal.example:80", false, false},
		{"host rule other port", []string{"api.example.com:443"}, "api.example.com:80", false, false},
		{"cidr rule other port", []string{"10.0.0.0/8:443"}, "secret.internal.example:80", false, false},
		{"host rule matches", []string{"api.example.com:443"}, "API.example.com:443", true, true},
		{"cidr rule could match", []string{"10.0.0.0/8"}, "db.internal.example:5432", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := NewAllowlist(c.specs)
			looked := false
			a.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
				looked = true
				return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}, nil
			}
			_, _, ok := a.Resolve(context.Background(), c.target)
			if looked != c.wantLookup {
				t.Fatalf("lookup performed=%v want %v", looked, c.wantLookup)
			}
			if ok != c.wantOK {
				t.Fatalf("Resolve ok=%v want %v", ok, c.wantOK)
			}
		})
	}
}

func (a *Allowlist) Allowed(target string) bool {
	_, _, ok := a.Resolve(context.Background(), target)
	return ok
}
