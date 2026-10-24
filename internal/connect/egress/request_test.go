package egress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/credseal"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type fixedDecider struct {
	decision authz.Decision
	err      error
	got      *authz.DecisionRequest
	scope    *identity.Scope
}

func (f *fixedDecider) Decider(scope identity.Scope) authz.Decider {
	*f.scope = scope
	return f
}

func (f *fixedDecider) Decide(_ context.Context, req authz.DecisionRequest) (authz.Decision, error) {
	*f.got = req
	return f.decision, f.err
}

type credentialTable map[string]*store.EgressCredential

func (c credentialTable) GetEgressCredential(_ context.Context, orgID, name string) (*store.EgressCredential, error) {
	if cred, ok := c[orgID+"/"+name]; ok {
		return cred, nil
	}
	return nil, store.ErrNotFound
}

type secretTable map[string]string

func (s secretTable) Open(_ context.Context, orgID, name string) ([]byte, error) {
	if v, ok := s[orgID+"/"+name]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("no such secret")
}

func credentialDecision(name string) authz.Decision {
	return authz.Decision{
		Allowed:    true,
		Reason:     authz.ReasonAllow,
		TTLSeconds: 30,
		Scope:      authz.CacheScope{Path: true},
		Policies:   []authz.DeterminingPolicy{{ID: "p1", Annotations: map[string]string{authz.CredentialAnnotation: name}}},
	}
}

func requestQuery(sealTo []byte, tls bool) tunnel.DecisionQuery {
	return tunnel.DecisionQuery{
		Action: tunnel.DecisionActionEgressRequest,
		HTTP:   &tunnel.HTTPQuery{Host: "API.Example.com", Port: 443, Method: "POST", Path: "/v1/charges", TLS: tls},
		SealTo: sealTo,
	}
}

func TestDecideRequestReleasesScopedSealedCredential(t *testing.T) {
	key, err := credseal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	agent := &store.Agent{ID: "ag_1", OrgID: "org_1"}
	scoped := &store.EgressCredential{OrgID: "org_1", Name: "billing", Header: "Authorization", Prefix: "Bearer ", Secret: "billing-key", AgentIDs: []string{"ag_1"}, Destinations: []string{"api.example.com"}}
	elsewhere := &store.EgressCredential{OrgID: "org_1", Name: "billing", Header: "Authorization", Secret: "billing-key", AgentIDs: []string{"ag_2"}, Destinations: []string{"api.example.com"}}
	elsewhereHost := &store.EgressCredential{OrgID: "org_1", Name: "billing", Header: "Authorization", Secret: "billing-key", AgentIDs: []string{"ag_1"}, Destinations: []string{"*.billing.example"}}
	secrets := secretTable{"org_1/billing-key": "sk_live_1"}

	cases := []struct {
		name     string
		decision authz.Decision
		deciderE error
		creds    credentialTable
		secrets  secretTable
		sealTo   []byte
		tls      bool
		allowed  bool
		reason   string
		sealed   bool
	}{
		{"injects for scoped agent", credentialDecision("billing"), nil, credentialTable{"org_1/billing": scoped}, secrets, key.PublicKey(), true, true, "allow", true},
		{"allow without credential", authz.Decision{Allowed: true, Reason: authz.ReasonAllow, TTLSeconds: 10}, nil, nil, nil, nil, false, true, "allow", false},
		{"policy deny", authz.Decision{Reason: authz.ReasonNoPolicyMatch}, nil, nil, nil, key.PublicKey(), true, false, string(authz.ReasonNoPolicyMatch), false},
		{"decider error fails closed", credentialDecision("billing"), errors.New("boom"), credentialTable{"org_1/billing": scoped}, secrets, key.PublicKey(), true, false, string(authz.ReasonUnavailable), false},
		{"agent outside scope", credentialDecision("billing"), nil, credentialTable{"org_1/billing": elsewhere}, secrets, key.PublicKey(), true, false, ReasonCredentialUnavailable, false},
		{"missing credential", credentialDecision("billing"), nil, credentialTable{}, secrets, key.PublicKey(), true, false, ReasonCredentialUnavailable, false},
		{"missing secret", credentialDecision("billing"), nil, credentialTable{"org_1/billing": scoped}, secretTable{}, key.PublicKey(), true, false, ReasonCredentialUnavailable, false},
		{"no seal key", credentialDecision("billing"), nil, credentialTable{"org_1/billing": scoped}, secrets, nil, true, false, ReasonCredentialUnavailable, false},
		{"destination outside credential scope", credentialDecision("billing"), nil, credentialTable{"org_1/billing": elsewhereHost}, secrets, key.PublicKey(), true, false, ReasonCredentialDestination, false},
		{"plaintext upstream", credentialDecision("billing"), nil, credentialTable{"org_1/billing": scoped}, secrets, key.PublicKey(), false, false, ReasonCredentialRequiresTLS, false},
		{"foreign org credential row", credentialDecision("billing"), nil, credentialTable{"org_1/billing": {OrgID: "org_2", Name: "billing", Header: "Authorization", Secret: "billing-key", AgentIDs: []string{"ag_1"}, Destinations: []string{"api.example.com"}}}, secrets, key.PublicKey(), true, false, ReasonCredentialUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got authz.DecisionRequest
			var scope identity.Scope
			h := New(Options{
				Conns:       memory.NewConnStore(),
				Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
				Deciders:    &fixedDecider{decision: tc.decision, err: tc.deciderE, got: &got, scope: &scope},
				Credentials: tc.creds,
				Secrets:     tc.secrets,
			})
			answer := h.DecideRequest(context.Background(), agent, requestQuery(tc.sealTo, tc.tls))
			if answer.Allowed != tc.allowed || answer.Reason != tc.reason {
				t.Fatalf("answer = %+v, want allowed=%v reason=%s", answer, tc.allowed, tc.reason)
			}
			if got.Action != authz.ActionEgressRequest || got.Resource.ID != "api.example.com:443" || got.Context.Method != "POST" ||
				got.Context.Path != "/v1/charges" || got.Context.TLS != tc.tls || got.Principal.ID != "ag_1" || scope.OrgID != "org_1" {
				t.Fatalf("decision request = %+v scope %+v", got, scope)
			}
			if !tc.sealed {
				if answer.Credential != nil {
					t.Fatalf("credential leaked: %+v", answer.Credential)
				}
				return
			}
			if answer.Credential == nil || answer.Credential.Header != "Authorization" || answer.Scope == nil || !answer.Scope.Path {
				t.Fatalf("answer = %+v", answer)
			}
			plain, err := key.Open(credseal.Binding{OrgID: "org_1", AgentID: "ag_1", Name: "billing"}, answer.Credential.Sealed)
			if err != nil || string(plain) != "Bearer sk_live_1" {
				t.Fatalf("open = %q, %v", plain, err)
			}
		})
	}
}

func TestDecideRequestRejectsMalformedQueries(t *testing.T) {
	h := New(Options{Conns: memory.NewConnStore(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Deciders: &fixedDecider{got: new(authz.DecisionRequest), scope: new(identity.Scope), decision: authz.Decision{Allowed: true}}})
	agent := &store.Agent{ID: "ag_1", OrgID: "org_1"}
	for name, q := range map[string]tunnel.DecisionQuery{
		"wrong action": {Action: tunnel.DecisionActionConnect, HTTP: &tunnel.HTTPQuery{Host: "a.example", Port: 443, Method: "GET", Path: "/"}},
		"no http":      {Action: tunnel.DecisionActionEgressRequest},
		"bad port":     {Action: tunnel.DecisionActionEgressRequest, HTTP: &tunnel.HTTPQuery{Host: "a.example", Port: 0, Method: "GET", Path: "/"}},
		"bad host":     {Action: tunnel.DecisionActionEgressRequest, HTTP: &tunnel.HTTPQuery{Host: "a b", Port: 443, Method: "GET", Path: "/"}},
	} {
		if answer := h.DecideRequest(context.Background(), agent, q); answer.Allowed || answer.Reason != string(authz.ReasonInvalidRequest) {
			t.Fatalf("%s: answer = %+v", name, answer)
		}
	}
}

func TestDecideRequestRefusesBadProxyCredentials(t *testing.T) {
	h := New(Options{Conns: memory.NewConnStore(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Deciders: &fixedDecider{got: new(authz.DecisionRequest), scope: new(identity.Scope), decision: authz.Decision{Allowed: true}}})
	q := requestQuery(nil, true)
	q.HTTP.Auth = "not-a-token"
	if answer := h.DecideRequest(context.Background(), &store.Agent{ID: "ag_1", OrgID: "org_1"}, q); answer.Allowed || answer.Reason != reasonNoAuth {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestDecideRequestNeverInjectsIntoTrace(t *testing.T) {
	key, err := credseal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	scoped := &store.EgressCredential{OrgID: "org_1", Name: "billing", Header: "Authorization", Secret: "billing-key", AgentIDs: []string{"ag_1"}, Destinations: []string{"api.example.com"}}
	h := New(Options{
		Conns:       memory.NewConnStore(),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Deciders:    &fixedDecider{decision: credentialDecision("billing"), got: new(authz.DecisionRequest), scope: new(identity.Scope)},
		Credentials: credentialTable{"org_1/billing": scoped},
		Secrets:     secretTable{"org_1/billing-key": "sk_live_1"},
	})
	q := requestQuery(key.PublicKey(), true)
	q.HTTP.Method = http.MethodTrace
	answer := h.DecideRequest(context.Background(), &store.Agent{ID: "ag_1", OrgID: "org_1"}, q)
	if answer.Allowed || answer.Reason != ReasonCredentialMethod || answer.Credential != nil {
		t.Fatalf("answer = %+v", answer)
	}
}
