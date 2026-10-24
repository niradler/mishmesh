package store

import (
	"strings"
	"testing"
)

func TestEndpointIdentityValidate(t *testing.T) {
	cred := func(header, secret, prefix string) *EndpointIdentity {
		return &EndpointIdentity{Credentials: map[string]EndpointCredential{"api": {Header: header, Secret: secret, Prefix: prefix}}}
	}
	tests := []struct {
		name string
		in   *EndpointIdentity
		ok   bool
	}{
		{"nil", nil, true},
		{"flags only", &EndpointIdentity{Required: true, AuthorizationHeader: true}, true},
		{"valid credential", cred("Authorization", "upstream-key", "Bearer "), true},
		{"bad secret name", cred("X-Api-Key", "Bad Name", ""), false},
		{"bad header", cred("X Api", "key", ""), false},
		{"reserved host", cred("host", "key", ""), false},
		{"reserved mishmesh header", cred("x-mishmesh-user", "key", ""), false},
		{"reserved forwarded header", cred("X-Forwarded-For", "key", ""), false},
		{"prefix newline", cred("X-Api-Key", "key", "a\r\nb"), false},
		{"prefix too long", cred("X-Api-Key", "key", strings.Repeat("a", 65)), false},
		{"bad credential name", &EndpointIdentity{Credentials: map[string]EndpointCredential{"Bad": {Header: "X-A", Secret: "k"}}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.in.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
