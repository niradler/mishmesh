package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/credseal"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	testOrgID   = "org-1"
	testAgentID = "agent-1"
)

type requestResult struct {
	inject *injection
	err    error
}

func (h *decisionHarness) requestAsync(req egressRequest) <-chan requestResult {
	out := make(chan requestResult, 1)
	go func() {
		inject, err := h.dec.checkRequest(context.Background(), req)
		out <- requestResult{inject, err}
	}()
	return out
}

func (h *decisionHarness) answerRequest(t *testing.T, answer func(tunnel.DecisionQuery) tunnel.DecisionAnswer) tunnel.DecisionQuery {
	t.Helper()
	msg, err := h.gateway.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != tunnel.MsgDecisionRequest || msg.Decision == nil {
		t.Fatalf("got %+v, want decision request", msg)
	}
	a := answer(*msg.Decision)
	a.ID = msg.Decision.ID
	h.dec.deliver(h.gen, a)
	return *msg.Decision
}

func waitRequest(t *testing.T, ch <-chan requestResult) requestResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("request decision did not return")
		return requestResult{}
	}
}

func sealedAnswer(t *testing.T, binding credseal.Binding, header, value string, scope tunnel.DecisionScope) func(tunnel.DecisionQuery) tunnel.DecisionAnswer {
	t.Helper()
	return func(q tunnel.DecisionQuery) tunnel.DecisionAnswer {
		sealed, err := credseal.Seal(q.SealTo, binding, []byte(value))
		if err != nil {
			t.Errorf("seal: %v", err)
		}
		return tunnel.DecisionAnswer{Allowed: true, TTLSeconds: 60, Scope: &scope, Credential: &tunnel.SealedCredential{Name: binding.Name, Header: header, Sealed: sealed}}
	}
}

func allowAnswer(scope tunnel.DecisionScope) func(tunnel.DecisionQuery) tunnel.DecisionAnswer {
	return func(tunnel.DecisionQuery) tunnel.DecisionAnswer {
		return tunnel.DecisionAnswer{Allowed: true, TTLSeconds: 60, Scope: &scope}
	}
}

func boundHarness(t *testing.T) *decisionHarness {
	t.Helper()
	h := newDecisionHarness(t)
	h.dec.bind(h.gen, testOrgID, testAgentID)
	return h
}

var httpsGet = egressRequest{host: "api.example.com", port: 443, method: "GET", path: "/v1/items", tls: true, auth: "tok-a"}

func TestCheckRequestFailsClosedBeforeTheSessionIsBound(t *testing.T) {
	h := newDecisionHarness(t)
	if _, err := h.dec.checkRequest(context.Background(), httpsGet); !errors.Is(err, errSessionUnbound) {
		t.Fatalf("err = %v, want errSessionUnbound", err)
	}
}

func TestCheckRequestOpensTheSealedCredential(t *testing.T) {
	h := boundHarness(t)
	res := h.requestAsync(httpsGet)
	binding := credseal.Binding{OrgID: testOrgID, AgentID: testAgentID, Name: "api-key"}
	q := h.answerRequest(t, sealedAnswer(t, binding, "authorization", "Bearer s3cret", tunnel.DecisionScope{Path: true, Method: true}))
	if q.Action != tunnel.DecisionActionEgressRequest || q.HTTP == nil || len(q.SealTo) != credseal.PublicKeySize {
		t.Fatalf("query = %+v", q)
	}
	want := tunnel.HTTPQuery{Host: "api.example.com", Port: 443, Method: "GET", Path: "/v1/items", TLS: true, Auth: "tok-a"}
	if *q.HTTP != want {
		t.Fatalf("http query = %+v, want %+v", *q.HTTP, want)
	}
	r := waitRequest(t, res)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if r.inject == nil || r.inject.header != "Authorization" || r.inject.value != "Bearer s3cret" {
		t.Fatalf("inject = %+v", r.inject)
	}
}

func TestCheckRequestRejectsUnusableCredentials(t *testing.T) {
	scope := tunnel.DecisionScope{}
	for _, tc := range []struct {
		name    string
		req     egressRequest
		binding credseal.Binding
		header  string
		value   string
		want    error
		reason  string
	}{
		{name: "sealed for another agent", req: httpsGet, binding: credseal.Binding{OrgID: testOrgID, AgentID: "agent-2", Name: "k"}, header: "X-Key", value: "v", want: errCredentialUnsealed},
		{name: "sealed for another org", req: httpsGet, binding: credseal.Binding{OrgID: "org-2", AgentID: testAgentID, Name: "k"}, header: "X-Key", value: "v", want: errCredentialUnsealed},
		{name: "header injection in value", req: httpsGet, binding: credseal.Binding{OrgID: testOrgID, AgentID: testAgentID, Name: "k"}, header: "X-Key", value: "v\r\nX-Evil: 1", want: errCredentialMalformed},
		{name: "invalid header name", req: httpsGet, binding: credseal.Binding{OrgID: testOrgID, AgentID: testAgentID, Name: "k"}, header: "X Key", value: "v", want: errCredentialMalformed},
		{name: "plaintext upstream", req: egressRequest{host: "api.example.com", port: 80, method: "GET", path: "/", auth: "tok-a"}, binding: credseal.Binding{OrgID: testOrgID, AgentID: testAgentID, Name: "k"}, header: "X-Key", value: "v", reason: "credential_requires_tls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := boundHarness(t)
			res := h.requestAsync(tc.req)
			h.answerRequest(t, sealedAnswer(t, tc.binding, tc.header, tc.value, scope))
			r := waitRequest(t, res)
			if r.inject != nil {
				t.Fatalf("inject = %+v, want none", r.inject)
			}
			var denied *DeniedError
			switch {
			case tc.reason != "":
				if !errors.As(r.err, &denied) || denied.Reason != tc.reason {
					t.Fatalf("err = %v, want denial %q", r.err, tc.reason)
				}
			case !errors.Is(r.err, tc.want):
				t.Fatalf("err = %v, want %v", r.err, tc.want)
			}
			res = h.requestAsync(tc.req)
			h.answerRequest(t, allowAnswer(scope))
			if r := waitRequest(t, res); r.err != nil || r.inject != nil {
				t.Fatalf("second request = %+v, want a fresh uncached allow", r)
			}
		})
	}
}

func TestCheckRequestCachesByScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  tunnel.DecisionScope
		second egressRequest
		asks   bool
	}{
		{name: "same request hits", scope: tunnel.DecisionScope{Path: true, Method: true}, second: httpsGet},
		{name: "path ignored when unscoped", scope: tunnel.DecisionScope{}, second: egressRequest{host: "API.example.com", port: 443, method: "POST", path: "/other", tls: true, auth: "tok-a"}},
		{name: "path scoped", scope: tunnel.DecisionScope{Path: true}, second: egressRequest{host: "api.example.com", port: 443, method: "GET", path: "/other", tls: true, auth: "tok-a"}, asks: true},
		{name: "method scoped", scope: tunnel.DecisionScope{Method: true}, second: egressRequest{host: "api.example.com", port: 443, method: "DELETE", path: "/v1/items", tls: true, auth: "tok-a"}, asks: true},
		{name: "other principal", scope: tunnel.DecisionScope{}, second: egressRequest{host: "api.example.com", port: 443, method: "GET", path: "/v1/items", tls: true, auth: "tok-b"}, asks: true},
		{name: "other exit", scope: tunnel.DecisionScope{}, second: egressRequest{host: "api.example.com", port: 443, method: "GET", path: "/v1/items", tls: true, auth: "tok-a", exit: "dc"}, asks: true},
		{name: "other port", scope: tunnel.DecisionScope{}, second: egressRequest{host: "api.example.com", port: 8443, method: "GET", path: "/v1/items", tls: true, auth: "tok-a"}, asks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := boundHarness(t)
			res := h.requestAsync(httpsGet)
			h.answerRequest(t, allowAnswer(tc.scope))
			if r := waitRequest(t, res); r.err != nil {
				t.Fatal(r.err)
			}
			res = h.requestAsync(tc.second)
			if tc.asks {
				h.answerRequest(t, func(tunnel.DecisionQuery) tunnel.DecisionAnswer {
					return tunnel.DecisionAnswer{Allowed: false, Reason: "policy_deny", TTLSeconds: 60}
				})
				var denied *DeniedError
				if r := waitRequest(t, res); !errors.As(r.err, &denied) {
					t.Fatalf("err = %v, want fresh denial", r.err)
				}
				return
			}
			if r := waitRequest(t, res); r.err != nil {
				t.Fatalf("cached err = %v", r.err)
			}
		})
	}
}

func TestCheckRequestFlushDropsCachedCredentials(t *testing.T) {
	h := boundHarness(t)
	flushed := make(chan struct{}, 1)
	h.dec.onFlush = func() { flushed <- struct{}{} }
	binding := credseal.Binding{OrgID: testOrgID, AgentID: testAgentID, Name: "k"}
	res := h.requestAsync(httpsGet)
	h.answerRequest(t, sealedAnswer(t, binding, "X-Key", "old", tunnel.DecisionScope{}))
	if r := waitRequest(t, res); r.err != nil || r.inject.value != "old" {
		t.Fatalf("first = %+v", r)
	}
	h.dec.flush()
	select {
	case <-flushed:
	default:
		t.Fatal("flush hook not called")
	}
	res = h.requestAsync(httpsGet)
	h.answerRequest(t, sealedAnswer(t, binding, "X-Key", "new", tunnel.DecisionScope{}))
	if r := waitRequest(t, res); r.err != nil || r.inject.value != "new" {
		t.Fatalf("after flush = %+v", r)
	}
	h.clock.Advance(61 * time.Second)
	res = h.requestAsync(httpsGet)
	h.answerRequest(t, allowAnswer(tunnel.DecisionScope{}))
	if r := waitRequest(t, res); r.err != nil || r.inject != nil {
		t.Fatalf("after ttl = %+v", r)
	}
}

func TestCheckRequestDetachForgetsTheSessionKey(t *testing.T) {
	h := boundHarness(t)
	h.dec.detach(h.gen)
	if _, err := h.dec.checkRequest(context.Background(), httpsGet); !errors.Is(err, errNotConnected) {
		t.Fatalf("err = %v, want errNotConnected", err)
	}
	h.dec.mu.Lock()
	defer h.dec.mu.Unlock()
	if h.dec.seal != nil || h.dec.binding != (sessionBinding{}) {
		t.Fatal("session key or binding survived detach")
	}
}

func TestDecisionCacheIsBounded(t *testing.T) {
	d := newRemoteDecider(time.Now)
	for i := range maxCachedDecisions + 10 {
		d.remember(0, 0, decisionKey{action: "x", port: i}, tunnel.DecisionAnswer{Allowed: true, TTLSeconds: 60}, nil)
	}
	if n := len(d.cache); n > maxCachedDecisions {
		t.Fatalf("cache size = %d, want <= %d", n, maxCachedDecisions)
	}
}

func TestPolicyPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/":              true,
		"/v1/items":      true,
		"/a/.well-known": true,
		"/a/../admin":    false,
		"/./x":           false,
		"/x/..":          false,
		"relative":       false,
		"/a\\b":          false,
		"":               false,
		"//admin":        false,
		"/api//admin":    false,
		"/admin;x=1":     false,
		"/a/b/":          true,
	} {
		if got := policyPath(path); got != want {
			t.Errorf("policyPath(%q) = %v, want %v", path, got, want)
		}
	}
}
