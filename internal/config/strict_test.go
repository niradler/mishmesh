package config

import (
	"strings"
	"testing"
)

func TestValidateRejectsMalformedEnv(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"int", "MISHMESH_TCP_PORT_MIN", "abc"},
		{"negative quota text", "MISHMESH_QUOTA_MAX_AGENTS", "ten"},
		{"bool", "MISHMESH_AUTH_ENABLED", "maybe"},
		{"duration", "MISHMESH_UDP_IDLE_TIMEOUT", "soon"},
		{"negative duration", "MISHMESH_ACCESS_LOG_RETENTION", "-1h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			t.Setenv("MISHMESH_API_AUTH_TOKEN", "tok")
			if err := LoadServer().Validate(); err == nil {
				t.Fatalf("%s=%q was accepted silently", tt.key, tt.value)
			}
		})
	}
}

func TestValidateReportsEveryMalformedEnv(t *testing.T) {
	t.Setenv("MISHMESH_API_AUTH_TOKEN", "tok")
	t.Setenv("MISHMESH_TCP_PORT_MIN", "abc")
	t.Setenv("MISHMESH_QUOTA_MAX_AGENTS", "ten")
	err := LoadServer().Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"MISHMESH_TCP_PORT_MIN", "MISHMESH_QUOTA_MAX_AGENTS"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %s", err, want)
		}
	}
}

func TestValidateRanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Server)
	}{
		{"tcp min above max", func(s *Server) { s.TCPEnabled, s.TCPPortMin, s.TCPPortMax = true, 20000, 10000 }},
		{"tcp port zero", func(s *Server) { s.TCPEnabled, s.TCPPortMin, s.TCPPortMax = true, 0, 10 }},
		{"tcp port too high", func(s *Server) { s.TCPEnabled, s.TCPPortMin, s.TCPPortMax = true, 10, 70000 }},
		{"udp min above max", func(s *Server) { s.IngressEnabled, s.UDPPortMin, s.UDPPortMax = true, 20000, 10000 }},
		{"negative agents quota", func(s *Server) { s.QuotaMaxAgents = -1 }},
		{"negative endpoints quota", func(s *Server) { s.QuotaMaxEndpoints = -1 }},
		{"negative bandwidth quota", func(s *Server) { s.QuotaMaxBandwidthBytes = -1 }},
		{"negative orgs per user", func(s *Server) { s.MaxOrgsPerUser = -1 }},
		{"negative udp flows", func(s *Server) { s.UDPMaxFlows = -1 }},
		{"negative session ttl", func(s *Server) { s.SessionTTLHours = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := baseValid()
			tt.mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateAcceptsDefaults(t *testing.T) {
	t.Setenv("MISHMESH_API_AUTH_TOKEN", "tok")
	if err := LoadServer().Validate(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
}
