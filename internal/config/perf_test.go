package config

import (
	"testing"
	"time"
)

func TestLoadServerPerfDefaultsAndOverrides(t *testing.T) {
	s := LoadServer()
	if s.IngressCacheTTL != 2*time.Second {
		t.Fatalf("IngressCacheTTL = %v, want 2s", s.IngressCacheTTL)
	}
	if s.PprofAddr != "" {
		t.Fatalf("PprofAddr = %q, want empty", s.PprofAddr)
	}
	t.Setenv("MISHMESH_INGRESS_CACHE_TTL", "0s")
	t.Setenv("MISHMESH_PPROF_ADDR", "127.0.0.1:6060")
	s = LoadServer()
	if s.IngressCacheTTL != 0 || s.PprofAddr != "127.0.0.1:6060" {
		t.Fatalf("overrides not applied: %v %q", s.IngressCacheTTL, s.PprofAddr)
	}
}
