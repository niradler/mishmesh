package controlplane

import (
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestBuildPolicyValidatesRateLimit(t *testing.T) {
	cases := []struct {
		name    string
		rl      *store.RateLimit
		wantErr bool
	}{
		{"valid", &store.RateLimit{Requests: 100, PeriodSeconds: 60, Burst: 10, Scope: "endpoint"}, false},
		{"no rate limit", nil, false},
		{"zero requests", &store.RateLimit{PeriodSeconds: 60}, true},
		{"bad scope", &store.RateLimit{Requests: 1, PeriodSeconds: 1, Scope: "global"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := &policyInput{EndpointPolicy: store.EndpointPolicy{RateLimit: c.rl}}
			pol, err := buildPolicy(in, nil)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && pol.RateLimit != c.rl {
				t.Fatalf("rate limit not preserved: %+v", pol.RateLimit)
			}
		})
	}
}
