package safehttp

import (
	"net"
	"testing"
)

func TestBlocked(t *testing.T) {
	cases := []struct {
		ip            string
		allowLoopback bool
		allowPrivate  bool
		blocked       bool
	}{
		{"169.254.169.254", false, false, true},
		{"169.254.169.254", true, true, true},
		{"127.0.0.1", false, false, true},
		{"::1", false, false, true},
		{"169.254.1.2", false, false, true},
		{"0.0.0.0", false, false, true},
		{"224.0.0.1", false, false, true},
		{"127.0.0.1", true, false, false},
		{"10.0.0.5", false, false, true},
		{"172.16.0.1", false, false, true},
		{"192.168.1.1", false, false, true},
		{"100.64.0.1", false, false, true},
		{"fc00::1", false, false, true},
		{"10.0.0.5", false, true, false},
		{"192.168.1.1", false, true, false},
		{"8.8.8.8", false, false, false},
		{"203.0.113.10", false, false, false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", tc.ip)
		}
		if got := Blocked(ip, tc.allowLoopback, tc.allowPrivate); got != tc.blocked {
			t.Errorf("Blocked(%s, loopback=%v, private=%v) = %v want %v", tc.ip, tc.allowLoopback, tc.allowPrivate, got, tc.blocked)
		}
	}
}
