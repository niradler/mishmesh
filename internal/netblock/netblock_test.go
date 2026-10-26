package netblock

import (
	"net"
	"testing"
)

func TestPolicyIPAllowed(t *testing.T) {
	_, allowed, err := net.ParseCIDR("10.1.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		policy Policy
		ip     string
		want   bool
	}{
		{"public", Policy{}, "8.8.8.8", true},
		{"public v6", Policy{}, "2606:4700::1111", true},
		{"loopback denied", Policy{}, "127.0.0.1", false},
		{"loopback allowed", Policy{AllowLoopback: true}, "::1", true},
		{"private denied", Policy{}, "10.0.0.1", false},
		{"private allowed", Policy{AllowPrivate: true}, "10.0.0.1", true},
		{"private via cidr", Policy{AllowedCIDRs: []*net.IPNet{allowed}}, "10.1.2.3", true},
		{"private outside cidr", Policy{AllowedCIDRs: []*net.IPNet{allowed}}, "10.2.2.3", false},
		{"metadata always blocked", Policy{AllowLoopback: true, AllowPrivate: true}, "169.254.169.254", false},
		{"aws v6 metadata always blocked", Policy{AllowLoopback: true, AllowPrivate: true}, "fd00:ec2::254", false},
		{"nat64 metadata blocked", Policy{AllowLoopback: true, AllowPrivate: true}, "64:ff9b::a9fe:a9fe", false},
		{"nat64 loopback needs loopback", Policy{AllowPrivate: true}, "64:ff9b::7f00:1", false},
		{"nat64 loopback allowed", Policy{AllowLoopback: true, AllowPrivate: true}, "64:ff9b::7f00:1", true},
		{"mapped metadata blocked", Policy{AllowLoopback: true, AllowPrivate: true}, "::ffff:169.254.169.254", false},
		{"broadcast blocked", Policy{AllowLoopback: true, AllowPrivate: true}, "255.255.255.255", false},
		{"this network blocked", Policy{AllowLoopback: true, AllowPrivate: true}, "0.1.2.3", false},
		{"benchmark range private", Policy{}, "198.18.0.1", false},
		{"cgnat private", Policy{}, "100.64.0.1", false},
		{"cgnat allowed", Policy{AllowPrivate: true}, "100.64.0.1", true},
		{"ula private", Policy{}, "fc00::1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("bad ip %q", tt.ip)
			}
			if got := tt.policy.IPAllowed(ip); got != tt.want {
				t.Fatalf("IPAllowed(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}
