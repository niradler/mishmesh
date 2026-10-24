package egress

import "testing"

func TestEgressClientIP(t *testing.T) {
	tests := []struct {
		reported string
		want     string
	}{
		{"192.168.1.20", "192.168.1.20"},
		{"::ffff:10.0.0.7", "10.0.0.7"},
		{"fe80::1%eth0", "fe80::1"},
		{"2001:db8::5", "2001:db8::5"},
		{"", agentClientIP},
		{"not-an-ip", agentClientIP},
		{"10.0.0.1:5555", agentClientIP},
	}
	for _, tt := range tests {
		if got := egressClientIP(tt.reported); got != tt.want {
			t.Errorf("egressClientIP(%q) = %q, want %q", tt.reported, got, tt.want)
		}
	}
}
