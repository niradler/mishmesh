package config

import "testing"

func TestPathRoutingDefault(t *testing.T) {
	tests := []struct {
		name       string
		signupMode string
		pathRoute  string
		want       bool
	}{
		{"org mode defaults off", "org", "", false},
		{"unset signup mode defaults off", "", "", false},
		{"invite mode defaults on", "invite", "", true},
		{"explicit on in org mode", "org", "true", true},
		{"explicit off in invite mode", "invite", "false", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.signupMode != "" {
				t.Setenv("MISHMESH_SIGNUP_MODE", tt.signupMode)
			}
			if tt.pathRoute != "" {
				t.Setenv("MISHMESH_PATH_ROUTING", tt.pathRoute)
			}
			if got := LoadServer().PathRouting; got != tt.want {
				t.Fatalf("PathRouting = %v, want %v", got, tt.want)
			}
		})
	}
}
