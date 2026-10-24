package store

import (
	"slices"
	"testing"
)

func validCredential(destinations ...string) *EgressCredential {
	return &EgressCredential{Name: "billing", Header: "Authorization", Secret: "billing-key", Destinations: destinations}
}

func TestEgressCredentialDestinationsAreRequiredAndNormalized(t *testing.T) {
	for name, destinations := range map[string][]string{
		"none":         nil,
		"zone":         {"fe80::1%eth0"},
		"bare star":    {"*"},
		"bad hostname": {"bad_host!"},
		"cidr":         {"10.0.0.0/8"},
	} {
		if err := validCredential(destinations...).Validate(); err == nil {
			t.Errorf("%s: %v accepted", name, destinations)
		}
	}
	c := validCredential("API.Example.com.", "*.Corp.Example", "[::ffff:10.0.0.1]", "api.example.com", "2001:DB8::1")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.corp.example", "10.0.0.1", "2001:db8::1", "api.example.com"}; !slices.Equal(c.Destinations, want) {
		t.Fatalf("destinations = %v, want %v", c.Destinations, want)
	}
}

func TestEgressCredentialReleasableTo(t *testing.T) {
	c := validCredential("api.example.com", "*.corp.example", "10.0.0.1")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{
		"api.example.com":      true,
		"API.EXAMPLE.COM.":     true,
		"x.api.example.com":    false,
		"evil.example.com":     false,
		"a.b.corp.example":     true,
		"corp.example":         false,
		"10.0.0.1":             true,
		"::ffff:10.0.0.1":      true,
		"10.0.0.2":             false,
		"":                     false,
		"api.example.com:443":  false,
		"api.example.com.evil": false,
	} {
		if got := c.ReleasableTo(host); got != want {
			t.Errorf("ReleasableTo(%q) = %v, want %v", host, got, want)
		}
	}
}
