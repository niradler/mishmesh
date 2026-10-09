package config

import (
	"testing"
	"time"
)

func baseValid() Server {
	return Server{BaseDomain: "localhost:8080", PublicScheme: "http", APIAuthToken: "tok", UpstreamResponseTimeout: 5 * time.Minute}
}

func TestUpstreamResponseTimeout(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"5m", 5 * time.Minute, true},
		{"4m", 4 * time.Minute, true},
		{"250ms", 250 * time.Millisecond, true},
		{"0", 0, false},
		{"-1s", -time.Second, false},
		{"invalid", -1, false},
		{"", -1, false},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("MISHMESH_UPSTREAM_RESPONSE_TIMEOUT", test.value)
			t.Setenv("MISHMESH_API_AUTH_TOKEN", "test-token")
			config := LoadServer()
			if config.UpstreamResponseTimeout != test.want {
				t.Fatalf("timeout = %v, want %v", config.UpstreamResponseTimeout, test.want)
			}
			if err := config.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate = %v, valid = %v", err, test.valid)
			}
		})
	}
}

func TestValidateAPIAuthFailClosed(t *testing.T) {
	s := baseValid()
	s.APIAuthToken = ""
	s.APIAuthDisabled = false
	if err := s.Validate(); err == nil {
		t.Fatal("expected error when API auth token missing and not explicitly disabled")
	}

	s.APIAuthDisabled = true
	if err := s.Validate(); err != nil {
		t.Fatalf("explicit opt-out should validate: %v", err)
	}

	s.APIAuthDisabled = false
	s.APIAuthToken = "secret"
	if err := s.Validate(); err != nil {
		t.Fatalf("token set should validate: %v", err)
	}
}

func TestValidateScheme(t *testing.T) {
	s := baseValid()
	s.PublicScheme = "ftp"
	if err := s.Validate(); err == nil {
		t.Fatal("expected error for invalid scheme")
	}
}

func validCluster() Server {
	s := baseValid()
	s.ClusterEnabled = true
	s.ConnBackend = "redis"
	s.RedisURL = "redis://127.0.0.1:6379"
	s.DataBackend = "postgres"
	s.NodeID = "node-1"
	s.RelayAddr = "127.0.0.1:7443"
	s.RelayAdvertise = "10.0.0.1:7443"
	s.ClusterSecret = "0123456789abcdef0123456789abcdef"
	return s
}

func TestValidateCluster(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Server)
		wantErr bool
	}{
		{"valid", func(*Server) {}, false},
		{"valid via postgres dsn", func(s *Server) { s.DataBackend = ""; s.DataDSN = "postgres://u@h/db" }, false},
		{"memory conn backend", func(s *Server) { s.ConnBackend = "memory" }, true},
		{"missing redis url", func(s *Server) { s.RedisURL = "" }, true},
		{"sqlite data backend", func(s *Server) { s.DataBackend = "sqlite" }, true},
		{"implicit sqlite", func(s *Server) { s.DataBackend = ""; s.DataDSN = "mishmesh.db" }, true},
		{"missing advertise", func(s *Server) { s.RelayAdvertise = "" }, true},
		{"missing relay addr", func(s *Server) { s.RelayAddr = "" }, true},
		{"missing node id", func(s *Server) { s.NodeID = "" }, true},
		{"short secret", func(s *Server) { s.ClusterSecret = "short" }, true},
		{"empty secret", func(s *Server) { s.ClusterSecret = "" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validCluster()
			tt.mutate(&s)
			err := s.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClusterDisabledSkipsClusterValidation(t *testing.T) {
	s := baseValid()
	s.ClusterSecret = ""
	if err := s.Validate(); err != nil {
		t.Fatalf("cluster off must not require cluster settings: %v", err)
	}
}

func TestValidateSignupMode(t *testing.T) {
	tests := []struct {
		mode    string
		wantErr bool
	}{
		{"", false},
		{"org", false},
		{"invite", false},
		{"open", true},
	}
	for _, tc := range tests {
		s := baseValid()
		s.SignupMode = tc.mode
		if err := s.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("mode %q: err=%v wantErr=%v", tc.mode, err, tc.wantErr)
		}
	}
}
