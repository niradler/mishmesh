package config

import "testing"

func TestDomainVerificationDefault(t *testing.T) {
	tests := []struct {
		name         string
		signupMode   string
		verification string
		want         bool
	}{
		{"org mode defaults on", "org", "", true},
		{"unset signup mode defaults on", "", "", true},
		{"invite mode defaults off", "invite", "", false},
		{"explicit off in org mode", "org", "false", false},
		{"explicit on in invite mode", "invite", "true", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.signupMode != "" {
				t.Setenv("MISHMESH_SIGNUP_MODE", tt.signupMode)
			}
			if tt.verification != "" {
				t.Setenv("MISHMESH_DOMAIN_VERIFICATION", tt.verification)
			}
			if got := LoadServer().DomainVerification; got != tt.want {
				t.Fatalf("DomainVerification = %v, want %v", got, tt.want)
			}
		})
	}
}
