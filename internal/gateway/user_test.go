package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type fakeUsers struct {
	mu     sync.Mutex
	byRaw  map[string]identity.Principal
	err    error
	subs   []func(identity.Revocation)
	gotTyp []token.Type
}

func (f *fakeUsers) Authenticate(_ context.Context, raw string, allowed ...token.Type) (identity.Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotTyp = allowed
	if f.err != nil {
		return identity.Principal{}, f.err
	}
	p, ok := f.byRaw[raw]
	if !ok {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return p, nil
}

func (f *fakeUsers) Subscribe(fn func(identity.Revocation)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, fn)
	return func() {}
}

func (f *fakeUsers) publish(rev identity.Revocation) {
	f.mu.Lock()
	subs := append([]func(identity.Revocation){}, f.subs...)
	f.mu.Unlock()
	for _, fn := range subs {
		fn(rev)
	}
}

type fakeDecider struct {
	answer tunnel.DecisionAnswer
	got    chan tunnel.DecisionQuery
}

func (f *fakeDecider) HandleConnect(context.Context, *store.Agent, net.Conn, tunnel.StreamInit) {}

func (f *fakeDecider) HandleEgress(context.Context, *store.Agent, net.Conn, tunnel.StreamInit) {}

func (f *fakeDecider) DecideRequest(_ context.Context, _ *store.Agent, q tunnel.DecisionQuery) tunnel.DecisionAnswer {
	f.got <- q
	return f.answer
}

func (f *fakeDecider) DecideConnect(_ context.Context, _ *store.Agent, q tunnel.DecisionQuery) tunnel.DecisionAnswer {
	f.got <- q
	return f.answer
}

func agentToken(t *testing.T, data store.DataStore, ag *store.Agent) string {
	t.Helper()
	raw, hash, err := store.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := data.CreateToken(context.Background(), &store.Token{ID: "tok-" + ag.ID, OrgID: ag.OrgID, AgentID: ag.ID, Hash: hash}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAgentConnectUserBinding(t *testing.T) {
	user := identity.Principal{OrgID: "org1", Ref: authz.EntityRef{Type: authz.EntityUser, ID: "u1"}, SessionID: "s1"}
	foreign := identity.Principal{OrgID: "org2", Ref: authz.EntityRef{Type: authz.EntityUser, ID: "u2"}, SessionID: "s2"}
	tests := []struct {
		name     string
		users    *fakeUsers
		header   string
		wantCode int
		wantBody string
	}{
		{"no users configured", nil, "anything", http.StatusUnauthorized, "user binding"},
		{"invalid user token", &fakeUsers{byRaw: map[string]identity.Principal{}}, "bad", http.StatusUnauthorized, "invalid user token"},
		{"identity store down", &fakeUsers{err: identity.ErrUnavailable}, "x", http.StatusServiceUnavailable, "temporarily unavailable"},
		{"foreign org", &fakeUsers{byRaw: map[string]identity.Principal{"f": foreign}}, "f", http.StatusForbidden, "another organization"},
		{"valid user token reaches websocket upgrade", &fakeUsers{byRaw: map[string]identity.Principal{"ok": user}}, "ok", http.StatusUpgradeRequired, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{}
			if tt.users != nil {
				opts.Users = tt.users
			}
			g, ag, _, data := newRegisterFixture(t, opts)
			raw := agentToken(t, data, ag)
			req := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
			req.Header.Set("Authorization", "Bearer "+raw)
			req.Header.Set(tunnel.UserTokenHeader, tt.header)
			rec := httptest.NewRecorder()
			g.HandleAgentConnect(rec, req)
			if tt.wantCode == http.StatusUpgradeRequired {
				if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusServiceUnavailable {
					t.Fatalf("status = %d, want to pass user binding (body %q)", rec.Code, rec.Body.String())
				}
				if len(tt.users.gotTyp) != 1 || tt.users.gotTyp[0] != token.TypeAgentUser {
					t.Fatalf("allowed token types = %v, want agent_user only", tt.users.gotTyp)
				}
				return
			}
			if rec.Code != tt.wantCode || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("status = %d body %q, want %d containing %q", rec.Code, rec.Body.String(), tt.wantCode, tt.wantBody)
			}
		})
	}
}

type liveAgent struct {
	sess *tunnel.Session
	ctrl *tunnel.Control
}

func dialAgent(t *testing.T, srv *httptest.Server, raw, userToken string) *liveAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := tunnel.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), tunnel.DialOptions{Token: raw, UserToken: userToken})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := tunnel.Client(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	ctrl, err := sess.OpenControl()
	if err != nil {
		t.Fatal(err)
	}
	return &liveAgent{sess: sess, ctrl: ctrl}
}

func (a *liveAgent) recvType(t *testing.T, want tunnel.MsgType) tunnel.ControlMessage {
	t.Helper()
	_ = a.ctrl.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer func() { _ = a.ctrl.SetReadDeadline(time.Time{}) }()
	for {
		msg, err := a.ctrl.Recv()
		if err != nil {
			t.Fatalf("waiting for %s: %v", want, err)
		}
		if msg.Type == want {
			return msg
		}
	}
}

func (a *liveAgent) ping(t *testing.T) {
	t.Helper()
	if err := a.ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgPing}); err != nil {
		t.Fatal(err)
	}
	a.recvType(t, tunnel.MsgPong)
}

func (a *liveAgent) expectNoMessage(t *testing.T) {
	t.Helper()
	_ = a.ctrl.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	defer func() { _ = a.ctrl.SetReadDeadline(time.Time{}) }()
	msg, err := a.ctrl.Recv()
	if err == nil {
		t.Fatalf("unexpected message %+v", msg)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestDecisionRequestAnsweredAndClamped(t *testing.T) {
	dec := &fakeDecider{answer: tunnel.DecisionAnswer{ID: 999, Allowed: true, TTLSeconds: 10_000}, got: make(chan tunnel.DecisionQuery, 1)}
	g, ag, _, data := newRegisterFixture(t, Options{Connect: dec})
	srv := httptest.NewServer(http.HandlerFunc(g.HandleAgentConnect))
	t.Cleanup(srv.Close)
	a := dialAgent(t, srv, agentToken(t, data, ag), "")

	q := tunnel.DecisionQuery{ID: 7, Action: tunnel.DecisionActionConnect, Connect: "db", Kind: "tcp"}
	if err := a.ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgDecisionRequest, Decision: &q}); err != nil {
		t.Fatal(err)
	}
	msg := a.recvType(t, tunnel.MsgDecisionResponse)
	if got := <-dec.got; !reflect.DeepEqual(got, q) {
		t.Fatalf("decider got %+v", got)
	}
	if msg.Answer == nil || msg.Answer.ID != 7 || !msg.Answer.Allowed || msg.Answer.TTLSeconds != tunnel.MaxDecisionTTLSeconds {
		t.Fatalf("answer = %+v", msg.Answer)
	}
}

func TestDecisionRequestDeniedWithoutDecider(t *testing.T) {
	g, ag, _, data := newRegisterFixture(t, Options{})
	srv := httptest.NewServer(http.HandlerFunc(g.HandleAgentConnect))
	t.Cleanup(srv.Close)
	a := dialAgent(t, srv, agentToken(t, data, ag), "")
	q := tunnel.DecisionQuery{ID: 3, Action: tunnel.DecisionActionConnect, Connect: "db", Kind: "tcp"}
	if err := a.ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgDecisionRequest, Decision: &q}); err != nil {
		t.Fatal(err)
	}
	msg := a.recvType(t, tunnel.MsgDecisionResponse)
	if msg.Answer == nil || msg.Answer.ID != 3 || msg.Answer.Allowed {
		t.Fatalf("answer = %+v, want deny", msg.Answer)
	}
}

func TestDecisionRequestDispatchedByAction(t *testing.T) {
	connect := &fakeDecider{answer: tunnel.DecisionAnswer{Allowed: true, Reason: "connect"}, got: make(chan tunnel.DecisionQuery, 4)}
	egress := &fakeDecider{answer: tunnel.DecisionAnswer{Allowed: true, Reason: "egress", Credential: &tunnel.SealedCredential{Name: "billing", Header: "Authorization", Sealed: []byte{1}}}, got: make(chan tunnel.DecisionQuery, 4)}
	g, ag, _, data := newRegisterFixture(t, Options{Connect: connect, Egress: egress})
	srv := httptest.NewServer(http.HandlerFunc(g.HandleAgentConnect))
	t.Cleanup(srv.Close)
	a := dialAgent(t, srv, agentToken(t, data, ag), "")

	cases := []struct {
		q      tunnel.DecisionQuery
		reason string
		via    *fakeDecider
	}{
		{tunnel.DecisionQuery{ID: 1, Action: tunnel.DecisionActionEgressRequest, HTTP: &tunnel.HTTPQuery{Host: "api.example.com", Port: 443, Method: "GET", Path: "/"}}, "egress", egress},
		{tunnel.DecisionQuery{ID: 2, Action: tunnel.DecisionActionConnect, Connect: "db", Kind: "tcp"}, "connect", connect},
		{tunnel.DecisionQuery{ID: 3, Action: "http:request"}, "unsupported", nil},
	}
	for _, tc := range cases {
		if err := a.ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgDecisionRequest, Decision: &tc.q}); err != nil {
			t.Fatal(err)
		}
		msg := a.recvType(t, tunnel.MsgDecisionResponse)
		if msg.Answer == nil || msg.Answer.ID != tc.q.ID || msg.Answer.Reason != tc.reason {
			t.Fatalf("query %d answer = %+v", tc.q.ID, msg.Answer)
		}
		if tc.via == nil {
			continue
		}
		if got := <-tc.via.got; !reflect.DeepEqual(got, tc.q) {
			t.Fatalf("query %d reached %+v", tc.q.ID, got)
		}
	}
	if len(connect.got) != 0 || len(egress.got) != 0 {
		t.Fatal("query reached the wrong decider")
	}
}

func TestFlushAllAndRevocationFlushBoundAgentsOnly(t *testing.T) {
	users := &fakeUsers{byRaw: map[string]identity.Principal{
		"ok": {OrgID: "org1", Ref: authz.EntityRef{Type: authz.EntityUser, ID: "u1"}, SessionID: "s1"},
	}}
	g, ag1, ag2, data := newRegisterFixture(t, Options{Users: users})
	srv := httptest.NewServer(http.HandlerFunc(g.HandleAgentConnect))
	t.Cleanup(srv.Close)
	bound := dialAgent(t, srv, agentToken(t, data, ag1), "ok")
	plain := dialAgent(t, srv, agentToken(t, data, ag2), "")
	bound.ping(t)
	plain.ping(t)

	g.FlushAll()
	bound.recvType(t, tunnel.MsgFlush)
	plain.recvType(t, tunnel.MsgFlush)

	users.publish(identity.Revocation{OrgID: "org2", SessionIDs: []string{"s1"}})
	bound.expectNoMessage(t)

	users.publish(identity.Revocation{OrgID: "org1", SessionIDs: []string{"other"}})
	bound.expectNoMessage(t)

	users.publish(identity.Revocation{OrgID: "org1", SessionIDs: []string{"s1"}})
	bound.recvType(t, tunnel.MsgFlush)
	plain.expectNoMessage(t)
}

type fakeCertIssuer struct {
	resp tunnel.CertResponse
	err  error
	got  chan []byte
}

func (f *fakeCertIssuer) IssueDecryptCert(_ context.Context, _ *store.Agent, csr []byte) (tunnel.CertResponse, error) {
	f.got <- csr
	return f.resp, f.err
}

func TestCertRequestAnswered(t *testing.T) {
	ok := &fakeCertIssuer{resp: tunnel.CertResponse{ID: 999, Chain: [][]byte{{1}}, Allow: []string{"api.example.com"}}, got: make(chan []byte, 1)}
	failing := &fakeCertIssuer{err: errors.New("store down: secret detail"), got: make(chan []byte, 1)}
	tests := []struct {
		name      string
		issuer    CertIssuer
		wantError string
		wantChain bool
	}{
		{"no issuer", nil, "egress decryption is not available on this server", false},
		{"issuer error is generic", failing, "certificate issuance unavailable", false},
		{"issued", ok, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{}
			if tt.issuer != nil {
				opts.Certs = tt.issuer
			}
			g, ag, _, data := newRegisterFixture(t, opts)
			srv := httptest.NewServer(http.HandlerFunc(g.HandleAgentConnect))
			t.Cleanup(srv.Close)
			a := dialAgent(t, srv, agentToken(t, data, ag), "")
			if err := a.ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgCertRequest, CertRequest: &tunnel.CertRequest{ID: 42, CSR: []byte("csr")}}); err != nil {
				t.Fatal(err)
			}
			msg := a.recvType(t, tunnel.MsgCertResponse)
			if msg.Cert == nil || msg.Cert.ID != 42 || msg.Cert.Error != tt.wantError || (len(msg.Cert.Chain) > 0) != tt.wantChain {
				t.Fatalf("cert = %+v", msg.Cert)
			}
			if fi, isFake := tt.issuer.(*fakeCertIssuer); isFake {
				if got := <-fi.got; string(got) != "csr" {
					t.Fatalf("issuer got csr %q", got)
				}
			}
		})
	}
}
