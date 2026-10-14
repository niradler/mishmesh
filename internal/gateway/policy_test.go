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

func TestDecodePolicyDropsControlPlaneFields(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := decodePolicy([]byte(`{"ip_allow":["10.0.0.0/8"],"proxy_targets":["169.254.169.254:80"],"load_balance":"failover","origin_tls":{"insecure_skip_verify":true},"service_id":"svc_other","public_access":"cloudflare","access":[{"connection_id":"conn_other","hostname":"private.example.test"}],"connect":{"name":"database","allow_agents":["ag_allowed","*"]}}`), log)
	if p == nil || len(p.IPAllow) != 1 {
		t.Fatalf("agent-owned fields lost: %+v", p)
	}
	if len(p.ProxyTargets) != 0 || p.LoadBalance != "" || p.OriginTLS != nil || p.ServiceID != "" || len(p.Access) > 0 {
		t.Fatalf("control-plane fields kept: %+v", p)
	}
	if p.Connect == nil || p.Connect.Name != "database" || len(p.Connect.AllowAgents) != 2 {
		t.Fatalf("authorized agent Connect was stripped: %+v", p.Connect)
	}
}
