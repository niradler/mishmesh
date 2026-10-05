package store

import "testing"

func TestRateLimitValidate(t *testing.T) {
	cases := []struct {
		name    string
		rl      RateLimit
		wantErr bool
	}{
		{"minimal", RateLimit{Requests: 10, PeriodSeconds: 60}, false},
		{"with burst and ip scope", RateLimit{Requests: 10, PeriodSeconds: 60, Burst: 20, Scope: "ip"}, false},
		{"endpoint scope", RateLimit{Requests: 10, PeriodSeconds: 1, Scope: "endpoint"}, false},
		{"zero requests", RateLimit{PeriodSeconds: 60}, true},
		{"zero period", RateLimit{Requests: 1}, true},
		{"negative burst", RateLimit{Requests: 1, PeriodSeconds: 1, Burst: -1}, true},
		{"unknown scope", RateLimit{Requests: 1, PeriodSeconds: 1, Scope: "org"}, true},
		{"absurd requests", RateLimit{Requests: 1 << 30, PeriodSeconds: 1}, true},
		{"absurd period", RateLimit{Requests: 1, PeriodSeconds: 1 << 30}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.rl.Validate(); (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}
