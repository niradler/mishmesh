package authz

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestShouldValidateDecisionRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DecisionRequest)
		ok     bool
	}{
		{"valid http request", func(*DecisionRequest) {}, true},
		{"control plane action", func(r *DecisionRequest) { r.Action = ActionPolicyWrite }, false},
		{"unknown action", func(r *DecisionRequest) { r.Action = "nope" }, false},
		{"agent principal on http", func(r *DecisionRequest) { r.Principal = EntityRef{Type: EntityAgent, ID: "ag_1"} }, false},
		{"anonymous principal on http", func(r *DecisionRequest) { r.Principal = EntityRef{Type: EntityAnonymous, ID: AnonymousID} }, true},
		{"group as principal", func(r *DecisionRequest) { r.Principal = EntityRef{Type: EntityGroup, ID: "grp_1"} }, false},
		{"destination resource on http", func(r *DecisionRequest) { r.Resource = EntityRef{Type: EntityDestination, ID: "a:1"} }, false},
		{"empty principal id", func(r *DecisionRequest) { r.Principal.ID = "" }, false},
		{"empty resource id", func(r *DecisionRequest) { r.Resource.ID = "" }, false},
		{"missing method", func(r *DecisionRequest) { r.Context.Method = "" }, false},
		{"missing path", func(r *DecisionRequest) { r.Context.Path = "" }, false},
		{"path too long", func(r *DecisionRequest) { r.Context.Path = "/" + strings.Repeat("a", maxPathLength) }, false},
		{"host too long", func(r *DecisionRequest) { r.Context.Host = strings.Repeat("a", maxHostLength+1) }, false},
		{"id too long", func(r *DecisionRequest) { r.Principal.ID = strings.Repeat("a", maxFieldLength+1) }, false},
		{"unknown auth method", func(r *DecisionRequest) { r.Context.AuthMethod = "magic" }, false},
		{"missing client ip", func(r *DecisionRequest) { r.Context.ClientIP = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httpRequest("/")
			tt.mutate(&req)
			err := req.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Validate() = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestShouldValidateConnectAndEgressRequests(t *testing.T) {
	connect := connectRequest()
	if err := connect.Validate(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	connect.Context.Protocol = ""
	if err := connect.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("connect without protocol: %v", err)
	}
	egress := DecisionRequest{
		Principal: EntityRef{Type: EntityUser, ID: "usr_1"},
		Action:    ActionEgressConnect,
		Resource:  EntityRef{Type: EntityDestination, ID: "example.com:443"},
		Context:   RequestContext{ClientIP: "10.0.0.1", AuthMethod: AuthMethodOIDC},
	}
	if err := egress.Validate(); err != nil {
		t.Fatalf("egress: %v", err)
	}
}

func TestShouldRoundTripWireTypes(t *testing.T) {
	authTime := time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC)
	req := httpRequest("/api")
	req.Context.AuthTime = &authTime
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDecisionRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Context.Path != "/api" || !decoded.Context.AuthTime.Equal(authTime) || decoded.Principal != req.Principal {
		t.Fatalf("decoded = %+v", decoded)
	}

	decision := Decision{Allowed: true, Reason: ReasonAllow, Policies: []DeterminingPolicy{{ID: "p", Annotations: map[string]string{"credential": "c"}}}, Owner: "org_1", PolicyVersion: "v", TTLSeconds: 60, Scope: CacheScope{Path: true}}
	raw, err = json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	var back Decision
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Reason != ReasonAllow || back.Policies[0].Annotations["credential"] != "c" || !back.Scope.Path {
		t.Fatalf("decision = %+v", back)
	}
}

func TestShouldRejectOversizeOrMalformedWireRequest(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"oversize", []byte(`{"pad":"` + strings.Repeat("a", MaxDecisionRequestBytes) + `"}`)},
		{"not json", []byte(`{`)},
		{"unknown field", []byte(`{"principal":{"type":"User","id":"u"},"action":"http:request","resource":{"type":"Endpoint","id":"e"},"context":{"client_ip":"10.0.0.1","method":"GET","path":"/","auth_method":"none","body":"x"}}`)},
		{"trailing data", []byte(`{"principal":{"type":"User","id":"u"},"action":"http:request","resource":{"type":"Endpoint","id":"e"},"context":{"client_ip":"10.0.0.1","method":"GET","path":"/","auth_method":"none"}} {}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeDecisionRequest(tt.raw); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
