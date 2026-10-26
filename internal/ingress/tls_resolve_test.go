package ingress

import (
	"context"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestResolveSNIOnlyReturnsPassthroughEndpoints(t *testing.T) {
	tp, data := newPassthroughForTest(t, time.Second)
	ctx := context.Background()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_1", Name: "o", CreatedAt: time.Now()})
	_ = data.CreateAgent(ctx, &store.Agent{ID: "ag_1", OrgID: "org_1", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()})
	endpoints := []*store.Endpoint{
		{ID: "ep_http_sub", Kind: store.KindHTTP, Subdomain: "web"},
		{ID: "ep_http_dom", Kind: store.KindHTTP, Domain: "web.corp.test"},
		{ID: "ep_tls_sub", Kind: store.KindTLS, Subdomain: "secure"},
		{ID: "ep_tls_dom", Kind: store.KindTLS, Domain: "secure.corp.test"},
	}
	for _, ep := range endpoints {
		ep.AgentID, ep.OrgID, ep.Lifecycle, ep.CreatedAt = "ag_1", "org_1", store.LifecycleEphemeral, time.Now()
		if err := data.CreateEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		sni  string
		want string
	}{
		{"web.example.com", ""},
		{"web.corp.test", ""},
		{"secure.example.com", "ep_tls_sub"},
		{"secure.corp.test", "ep_tls_dom"},
		{"ghost.example.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.sni, func(t *testing.T) {
			got := ""
			if ep := tp.resolveSNI(tt.sni); ep != nil {
				got = ep.ID
			}
			if got != tt.want {
				t.Fatalf("resolveSNI(%q) = %q, want %q", tt.sni, got, tt.want)
			}
		})
	}
}
