package proxy

import "testing"

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Target
		wantErr bool
	}{
		{"bare", "example.com:8080", Target{"http", "example.com", "8080"}, false},
		{"http", "http://Example.com:80", Target{"http", "example.com", "80"}, false},
		{"https", "https://127.0.0.1:9443", Target{"https", "127.0.0.1", "9443"}, false},
		{"ipv6", "https://[::1]:443", Target{"https", "::1", "443"}, false},
		{"trailing dot", "example.com.:80", Target{"http", "example.com", "80"}, false},
		{"missing port", "example.com", Target{}, true},
		{"missing port scheme", "https://example.com", Target{}, true},
		{"path", "https://example.com:443/x", Target{}, true},
		{"root path", "https://example.com:443/", Target{}, true},
		{"query", "https://example.com:443?x=1", Target{}, true},
		{"userinfo", "https://u:p@example.com:443", Target{}, true},
		{"bare path", "example.com:80/x", Target{}, true},
		{"bad scheme", "ftp://example.com:21", Target{}, true},
		{"port zero", "example.com:0", Target{}, true},
		{"port high", "example.com:70000", Target{}, true},
		{"empty host", ":80", Target{}, true},
		{"empty", "", Target{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTarget(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestTargetHelpers(t *testing.T) {
	tg, err := ParseTarget("https://[::1]:443")
	if err != nil {
		t.Fatal(err)
	}
	if !tg.TLS() || tg.Addr() != "[::1]:443" {
		t.Fatalf("TLS=%v Addr=%q", tg.TLS(), tg.Addr())
	}
	plain, _ := ParseTarget("a.example.com:80")
	if plain.TLS() {
		t.Fatal("bare target must not be TLS")
	}
}

func TestGuardAcceptsAllTargetForms(t *testing.T) {
	g := DefaultGuard()
	for _, target := range []string{"93.184.216.34:443", "http://93.184.216.34:80", "https://93.184.216.34:443"} {
		if err := g.ValidateTarget(target); err != nil {
			t.Fatalf("ValidateTarget(%q): %v", target, err)
		}
		if _, err := g.Resolve(t.Context(), target); err != nil {
			t.Fatalf("Resolve(%q): %v", target, err)
		}
	}
	if err := g.ValidateTarget("https://127.0.0.1:443"); err == nil {
		t.Fatal("loopback https target must stay blocked")
	}
	if err := g.ValidateTarget("https://93.184.216.34:443/x"); err == nil {
		t.Fatal("path must be rejected")
	}
}
