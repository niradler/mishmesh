package config

import (
	"testing"
	"time"
)

func TestLoadServerDataPoolDefaults(t *testing.T) {
	s := LoadServer()
	if s.DataMaxConns != 25 {
		t.Fatalf("DataMaxConns = %d, want 25", s.DataMaxConns)
	}
	if s.DataConnMaxLifetime != 30*time.Minute || s.DataConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("lifetime/idle = %v/%v", s.DataConnMaxLifetime, s.DataConnMaxIdleTime)
	}
}

func TestLoadServerDataPoolOverrides(t *testing.T) {
	tests := []struct {
		name         string
		maxConns     string
		lifetime     string
		wantConns    int
		wantLifetime time.Duration
	}{
		{"explicit", "40", "10m", 40, 10 * time.Minute},
		{"bad values fall back", "x", "nope", 25, 30 * time.Minute},
		{"negative duration falls back", "7", "-1s", 7, 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MISHMESH_DATA_MAX_CONNS", tt.maxConns)
			t.Setenv("MISHMESH_DATA_CONN_MAX_LIFETIME", tt.lifetime)
			s := LoadServer()
			if s.DataMaxConns != tt.wantConns || s.DataConnMaxLifetime != tt.wantLifetime {
				t.Fatalf("got %d/%v, want %d/%v", s.DataMaxConns, s.DataConnMaxLifetime, tt.wantConns, tt.wantLifetime)
			}
		})
	}
}

func TestValidateRejectsNegativePool(t *testing.T) {
	s := baseValid()
	s.DataMaxConns = -1
	if err := s.Validate(); err == nil {
		t.Fatal("expected error for negative DATA_MAX_CONNS")
	}
}
