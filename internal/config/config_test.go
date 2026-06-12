package config

import "testing"

func baseValid() Server {
	return Server{BaseDomain: "localhost:8080", PublicScheme: "http", APIAuthToken: "tok"}
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
