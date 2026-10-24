package controlplane

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/orgsecret"
)

func TestEgressCredentialsAPI(t *testing.T) {
	f := newTenantFixture(t)
	f.api.SetSecretVault(orgsecret.New(f.api.data, testSecretBox(t)))
	var flushed []string
	f.api.SetDecisionFlusher(func(org string) { flushed = append(flushed, org) })

	var bobAgent struct {
		Agent agentDTO `json:"agent"`
	}
	doc(t, f.bob, f.srv, http.MethodPost, "/api/v1/agents", `{"name":"bob-agent"}`, http.StatusCreated, &bobAgent)

	invalid := []struct {
		name, path, body string
	}{
		{"bad name", "/api/v1/egress/credentials/Bad.Name", `{"header":"Authorization","secret":"k","agent_ids":[],"destinations":["api.billing.test"]}`},
		{"bad header", "/api/v1/egress/credentials/billing", `{"header":"Bad Header","secret":"k","agent_ids":[],"destinations":["api.billing.test"]}`},
		{"reserved header", "/api/v1/egress/credentials/billing", `{"header":"Host","secret":"k","agent_ids":[],"destinations":["api.billing.test"]}`},
		{"mishmesh header", "/api/v1/egress/credentials/billing", `{"header":"X-Mishmesh-Agent","secret":"k","agent_ids":[],"destinations":["api.billing.test"]}`},
		{"bad secret name", "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"Bad.Secret","agent_ids":[],"destinations":["api.billing.test"]}`},
		{"newline prefix", "/api/v1/egress/credentials/billing", `{"header":"Authorization","prefix":"a\nb","secret":"k","agent_ids":[],"destinations":["api.billing.test"]}`},
		{"unknown agent", "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"k","agent_ids":["ag_missing"],"destinations":["api.billing.test"]}`},
		{"no destinations", "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"k","agent_ids":[]}`},
		{"cidr destination", "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"k","agent_ids":[],"destinations":["10.0.0.0/8"]}`},
		{"foreign agent", "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"k","agent_ids":["` + bobAgent.Agent.ID + `"],"destinations":["api.billing.test"]}`},
		{"unknown field", "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"k","value":"leak"}`},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if status, body := f.do(t, f.alice, http.MethodPut, tc.path, tc.body); status != http.StatusBadRequest {
				t.Fatalf("status %d: %s", status, body)
			}
		})
	}
	if len(flushed) != 0 {
		t.Fatalf("rejected writes flushed: %v", flushed)
	}

	var cred egressCredentialDTO
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/egress/credentials/billing",
		`{"header":"Authorization","prefix":"Bearer ","secret":"billing-key","agent_ids":["`+f.agentA+`","`+f.agentA+`"],"destinations":["API.billing.test","*.billing.test"]}`, http.StatusOK, &cred)
	if cred.Name != "billing" || cred.Header != "Authorization" || !slices.Equal(cred.AgentIDs, []string{f.agentA}) || !slices.Equal(cred.Destinations, []string{"*.billing.test", "api.billing.test"}) || cred.CreatedAt.IsZero() {
		t.Fatalf("put = %+v", cred)
	}
	if !slices.Equal(flushed, []string{f.orgA}) {
		t.Fatalf("flushes after put = %v", flushed)
	}

	var got egressCredentialDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/egress/credentials/billing", "", http.StatusOK, &got)
	if got.Secret != "billing-key" || got.Prefix != "Bearer " {
		t.Fatalf("get = %+v", got)
	}
	var list []egressCredentialDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/egress/credentials", "", http.StatusOK, &list)
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}

	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/egress/credentials/billing", ""},
		{http.MethodDelete, "/api/v1/egress/credentials/billing", ""},
	} {
		if status, _ := f.do(t, f.bob, req.method, req.path, req.body); status != http.StatusNotFound {
			t.Fatalf("bob %s %s = %d", req.method, req.path, status)
		}
	}
	if status, body := f.do(t, f.bob, http.MethodGet, "/api/v1/egress/credentials", ""); status != http.StatusOK || strings.Contains(body, "billing") {
		t.Fatalf("bob list = %d %s", status, body)
	}

	flushed = nil
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/secrets/billing-key", `{"value":"sk_live_1"}`, http.StatusOK, nil)
	doc(t, f.alice, f.srv, http.MethodDelete, "/api/v1/secrets/billing-key", "", http.StatusNoContent, nil)
	doc(t, f.alice, f.srv, http.MethodDelete, "/api/v1/egress/credentials/billing", "", http.StatusNoContent, nil)
	doc(t, f.alice, f.srv, http.MethodDelete, "/api/v1/egress/credentials/billing", "", http.StatusNotFound, nil)
	if !slices.Equal(flushed, []string{f.orgA, f.orgA, f.orgA}) {
		t.Fatalf("flushes after secret and credential changes = %v", flushed)
	}
}

func TestEgressCredentialsRequirePolicyWrite(t *testing.T) {
	srv := newAuthAPI(t)
	owner := registerClient(t, srv, "owner@example.com", "owner")
	member := inviteAndRegister(t, srv, owner, "member@example.com", "member")
	doc(t, member, srv, http.MethodPut, "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"k","agent_ids":[],"destinations":["api.billing.test"]}`, http.StatusForbidden, nil)
	doc(t, member, srv, http.MethodDelete, "/api/v1/egress/credentials/billing", "", http.StatusForbidden, nil)
	doc(t, owner, srv, http.MethodPut, "/api/v1/egress/credentials/billing", `{"header":"Authorization","secret":"k","agent_ids":[],"destinations":["api.billing.test"]}`, http.StatusOK, nil)
}
