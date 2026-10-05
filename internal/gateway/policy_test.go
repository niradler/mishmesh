package gateway

import (
	"io"
	"log/slog"
	"testing"
)

func TestDecodePolicyDropsInvalidRateLimitKeepsRest(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := decodePolicy([]byte(`{"ip_allow":["10.0.0.0/8"],"rate_limit":{"requests":0,"period_seconds":1}}`), log)
	if p == nil || len(p.IPAllow) != 1 || p.RateLimit != nil {
		t.Fatalf("policy = %+v", p)
	}
	p = decodePolicy([]byte(`{"rate_limit":{"requests":5,"period_seconds":1}}`), log)
	if p == nil || p.RateLimit == nil || p.RateLimit.Requests != 5 {
		t.Fatalf("valid rate limit lost: %+v", p)
	}
}
