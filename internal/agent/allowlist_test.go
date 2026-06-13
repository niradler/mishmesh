package agent

import "testing"

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
	dialAddr, serverName, ok := a.Resolve("8.8.8.8:53")
	if !ok || dialAddr != "8.8.8.8:53" || serverName != "8.8.8.8" {
		t.Fatalf("Resolve pinning: addr=%q sni=%q ok=%v", dialAddr, serverName, ok)
	}
	if _, _, ok := a.Resolve("169.254.169.254:53"); ok {
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
