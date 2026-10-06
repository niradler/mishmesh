package subdomain

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		sub  string
		base string
		ok   bool
	}{
		{"plain", "shop", "mishmesh.io", true},
		{"digits and hyphen", "a-1-b", "mishmesh.io", true},
		{"single char", "a", "", true},
		{"max length", "a23456789012345678901234567890123456789012345678901234567890123", "", true},
		{"too long", "a234567890123456789012345678901234567890123456789012345678901234", "", false},
		{"empty", "", "", false},
		{"uppercase", "Shop", "", false},
		{"dot", "a.b", "", false},
		{"leading hyphen", "-a", "", false},
		{"trailing hyphen", "a-", "", false},
		{"underscore", "a_b", "", false},
		{"space", "a b", "", false},
		{"slash", "a/b", "", false},
		{"unicode", "café", "", false},
		{"reserved app", "app", "mishmesh.io", false},
		{"reserved api", "api", "", false},
		{"reserved www", "www", "", false},
		{"reserved admin", "admin", "", false},
		{"reserved login", "login", "", false},
		{"reserved auth", "auth", "", false},
		{"reserved mail", "mail", "", false},
		{"reserved status", "status", "", false},
		{"reserved docs", "docs", "", false},
		{"reserved static", "static", "", false},
		{"reserved assets", "assets", "", false},
		{"reserved cdn", "cdn", "", false},
		{"reserved connect", "connect", "", false},
		{"reserved dashboard", "dashboard", "", false},
		{"reserved console", "console", "", false},
		{"configured host label", "portal", "portal.mishmesh.io", false},
		{"configured host label with port", "localhost", "localhost:8080", false},
		{"other label with host base", "shop", "portal.mishmesh.io", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.sub, tc.base)
			if (err == nil) != tc.ok {
				t.Fatalf("Validate(%q,%q) err=%v want ok=%v", tc.sub, tc.base, err, tc.ok)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("  ShOp "); got != "shop" {
		t.Fatalf("got %q", got)
	}
}

func TestHostLabel(t *testing.T) {
	tests := map[string]string{
		"app.mishmesh.io":  "app",
		"localhost:8080":   "localhost",
		"Portal.Example.":  "portal",
		"mishmesh.io:8443": "mishmesh",
		"":                 "",
	}
	for in, want := range tests {
		if got := HostLabel(in); got != want {
			t.Errorf("HostLabel(%q)=%q want %q", in, got, want)
		}
	}
}
