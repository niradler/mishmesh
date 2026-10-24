package authz

import (
	"strings"
	"testing"
)

func TestNormalizeHost(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"Example.COM", "example.com", true},
		{"  api.example.com.  ", "api.example.com", true},
		{"10.0.0.1", "10.0.0.1", true},
		{"::ffff:10.0.0.1", "10.0.0.1", true},
		{"[2001:DB8::1]", "2001:db8::1", true},
		{"0.0.0.0", "0.0.0.0", true},
		{"", "", false},
		{".", "", false},
		{"a..b", "", false},
		{".a.b", "", false},
		{"a\x00b", "", false},
		{`a\b`, "", false},
		{"a/b", "", false},
		{"user@host", "", false},
		{"fe80::1%eth0", "", false},
		{strings.Repeat("a", 254), "", false},
		{strings.Repeat("a", 253), strings.Repeat("a", 253), true},
	}
	for _, tt := range tests {
		got, err := NormalizeHost(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestNormalizeDestination(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"API.example.com.:443", "api.example.com:443", true},
		{"[::1]:22", "[::1]:22", true},
		{"10.0.0.1:80", "10.0.0.1:80", true},
		{"example.com", "", false},
		{"example.com:0", "", false},
		{"example.com:65536", "", false},
		{"example.com:0443", "", false},
		{"example.com:+1", "", false},
		{":443", "", false},
	}
	for _, tt := range tests {
		got, err := NormalizeDestination(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("NormalizeDestination(%q) = %q, %v; want %q ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestShouldRejectUnnormalizedDestination(t *testing.T) {
	az := mustAuthorizer(t, `permit (principal, action == Action::"egress:connect", resource);`)
	req := DecisionRequest{
		Principal: EntityRef{Type: EntityAgent, ID: "ag_1"},
		Action:    ActionEgressConnect,
		Resource:  EntityRef{Type: EntityDestination, ID: "API.example.com:443"},
		Context:   RequestContext{ClientIP: "10.0.0.1", AuthMethod: AuthMethodAgentToken},
	}
	facts := connectFacts(EndpointEntity{})
	facts.Endpoint = nil
	if got := az.Decide(req, facts); got.Reason != ReasonInvalidRequest {
		t.Fatalf("reason = %q, want invalid_request", got.Reason)
	}
	req.Resource.ID = "api.example.com:443"
	if got := az.Decide(req, facts); !got.Allowed {
		t.Fatalf("normalized destination denied: %+v", got)
	}
}
