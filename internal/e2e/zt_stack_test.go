package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/accesslog"
	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/bridge"
	"github.com/mishmesh/mishmesh/internal/connect/egress"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/ingress"
	"github.com/mishmesh/mishmesh/internal/orgsecret"
	"github.com/mishmesh/mishmesh/internal/safehttp"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/token"
	"github.com/mishmesh/mishmesh/internal/tunnel"
	"github.com/mishmesh/mishmesh/internal/workload"
)

const ztPassword = "supersecret"

var errZTDecidersDown = errors.New("decision service down")

type ztConnect struct {
	bridge    *bridge.Bridge
	decisions atomic.Int64
}

func (c *ztConnect) HandleConnect(ctx context.Context, ag *store.Agent, stream net.Conn, init tunnel.StreamInit) {
	c.bridge.HandleConnect(ctx, ag, stream, init)
}

func (c *ztConnect) DecideConnect(ctx context.Context, ag *store.Agent, q tunnel.DecisionQuery) tunnel.DecisionAnswer {
	c.decisions.Add(1)
	return c.bridge.DecideConnect(ctx, ag, q)
}

type ztDeciders struct {
	inner identity.Deciders
	down  atomic.Bool
}

func (d *ztDeciders) Decider(scope identity.Scope) authz.Decider {
	if d.down.Load() {
		return ztDownDecider{}
	}
	return d.inner.Decider(scope)
}

type ztDownDecider struct{}

func (ztDownDecider) Decide(context.Context, authz.DecisionRequest) (authz.Decision, error) {
	return authz.Decision{Reason: authz.ReasonUnavailable}, errZTDecidersDown
}

type ztStack struct {
	t        *testing.T
	ctx      context.Context
	log      *slog.Logger
	data     store.DataStore
	conns    *memory.ConnStore
	identity *identity.Service
	connect  *ztConnect
	deciders *ztDeciders
	api      *httptest.Server
	ingress  *httptest.Server
	wsURL    string
}

func newZTStack(t *testing.T, verifierClient *http.Client) *ztStack {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "zt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()

	keys, err := token.NewKeyring(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	idSvc := identity.New(keys, memory.NewSessionStore())
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	vault := orgsecret.New(data, box)

	if verifierClient == nil {
		verifierClient = safehttp.Client(true, true)
	}
	cp := controlplane.New(data, conns, "", log)
	cp.SetSecretVault(vault)
	cp.SetIdentity(idSvc)
	idSvc.OnFlush(cp.ClearAuthzCache)
	cp.SetWorkloadVerifier(workload.NewVerifier(workload.Options{Client: verifierClient, AllowHTTP: true}))

	deciders := &ztDeciders{inner: cp}
	connect := &ztConnect{bridge: bridge.New(bridge.Options{Data: data, Conns: conns, Log: log, Deciders: deciders, Revocations: idSvc})}
	guard, err := proxy.NewGuard(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	gw := gateway.New(gateway.Options{
		Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http",
		Users: idSvc, Connect: connect,
		Egress: egress.New(egress.Options{Conns: conns, Log: log, Deciders: deciders, Dialer: guard, Auth: idSvc, Agents: data, Revocations: idSvc, Credentials: data, Secrets: vault}),
	})
	idSvc.OnFlush(gw.FlushOrg)
	cp.SetDecisionFlusher(idSvc.FlushOrg)
	cp.SetPublicConfig("localhost", "http")
	cp.ConfigureAuth(controlplane.AuthOptions{Enabled: true, PasswordEnabled: true})

	mux := http.NewServeMux()
	mux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	cp.Register(mux)
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)

	logger := accesslog.New(data, accesslog.Options{FlushInterval: 50 * time.Millisecond, Log: log})
	logCtx, stopLog := context.WithCancel(ctx)
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		logger.Run(logCtx)
	}()
	t.Cleanup(func() {
		stopLog()
		<-logDone
	})

	ing := ingress.New(ingress.Options{
		Data: data, Conns: conns, Log: log, BaseDomain: "localhost",
		Identity: idSvc, Deciders: cp, APIPublicURL: api.URL, AccessLog: logger, Secrets: vault,
	})
	cp.SetIngressInvalidator(ing.InvalidateCaches)
	ingSrv := httptest.NewServer(ing)
	t.Cleanup(ingSrv.Close)

	return &ztStack{
		t: t, ctx: ctx, log: log, data: data, conns: conns, identity: idSvc,
		connect: connect, deciders: deciders, api: api, ingress: ingSrv,
		wsURL: "ws" + strings.TrimPrefix(api.URL, "http"),
	}
}

type ztUser struct {
	s      *ztStack
	client *http.Client
	id     string
	org    string
	email  string
}

func (s *ztStack) register(email, invite string) *ztUser {
	s.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		s.t.Fatal(err)
	}
	u := &ztUser{s: s, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}, email: email}
	var me struct {
		ID          string `json:"id"`
		ActiveOrgID string `json:"active_org_id"`
	}
	u.mustJSON(http.MethodPost, "/api/v1/auth/register", map[string]string{"email": email, "password": ztPassword, "name": "z", "invite_token": invite}, http.StatusCreated, &me)
	u.id, u.org = me.ID, me.ActiveOrgID
	return u
}

func (u *ztUser) invite(email string) *ztUser {
	u.s.t.Helper()
	var inv struct {
		InviteToken string `json:"invite_token"`
	}
	u.mustJSON(http.MethodPost, "/api/v1/members", map[string]string{"email": email, "role": store.RoleMember}, http.StatusCreated, &inv)
	member := u.s.register(email, inv.InviteToken)
	member.org = u.org
	return member
}

func (u *ztUser) do(method, path string, body any) (int, []byte) {
	u.s.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			u.s.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u.s.api.URL+path, rd)
	if err != nil {
		u.s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := u.client.Do(req)
	if err != nil {
		u.s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (u *ztUser) mustJSON(method, path string, body any, want int, out any) {
	u.s.t.Helper()
	status, raw := u.do(method, path, body)
	if status != want {
		u.s.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			u.s.t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
}

func (u *ztUser) createAgent(name string) (id, rawToken string) {
	u.s.t.Helper()
	var out struct {
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
		Token string `json:"token"`
	}
	u.mustJSON(http.MethodPost, "/api/v1/agents", map[string]string{"name": name}, http.StatusCreated, &out)
	return out.Agent.ID, out.Token
}

type ztServiceAccount struct {
	id       string
	clientID string
	secret   string
}

func (u *ztUser) createServiceAccount(name string) ztServiceAccount {
	u.s.t.Helper()
	var out struct {
		ServiceAccount struct {
			ID       string `json:"id"`
			ClientID string `json:"client_id"`
		} `json:"service_account"`
		ClientSecret string `json:"client_secret"`
	}
	u.mustJSON(http.MethodPost, "/api/v1/service-accounts", map[string]string{"name": name, "role": store.RoleMember}, http.StatusCreated, &out)
	return ztServiceAccount{id: out.ServiceAccount.ID, clientID: out.ServiceAccount.ClientID, secret: out.ClientSecret}
}

func (u *ztUser) createGroup(name string) string {
	u.s.t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	u.mustJSON(http.MethodPost, "/api/v1/groups", map[string]string{"name": name}, http.StatusCreated, &out)
	return out.ID
}

func (u *ztUser) addGroupMember(groupID, memberType, memberID string) {
	u.s.t.Helper()
	u.mustJSON(http.MethodPost, "/api/v1/groups/"+groupID+"/members", map[string]string{"type": memberType, "id": memberID}, http.StatusNoContent, nil)
}

func (u *ztUser) putPolicy(extra string) {
	u.s.t.Helper()
	u.mustJSON(http.MethodPut, "/api/v1/policy", map[string]string{"cedar_src": string(authz.DefaultPolicySource()) + "\n" + extra}, http.StatusOK, nil)
}

func (u *ztUser) putSecret(name, value string) {
	u.s.t.Helper()
	status, raw := u.do(http.MethodPut, "/api/v1/secrets/"+name, map[string]string{"value": value})
	if status/100 != 2 {
		u.s.t.Fatalf("put secret %s: status %d: %s", name, status, raw)
	}
}

func (u *ztUser) accessLog(query url.Values) []ztAccessEvent {
	u.s.t.Helper()
	var out []ztAccessEvent
	u.mustJSON(http.MethodGet, "/api/v1/access-log?"+query.Encode(), nil, http.StatusOK, &out)
	return out
}

type ztAccessEvent struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	EndpointID    string `json:"endpoint_id"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Status        int    `json:"status"`
}

func (s *ztStack) tokenRequest(form url.Values) (int, string) {
	s.t.Helper()
	resp, err := http.PostForm(s.api.URL+"/oauth2/token", form)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out.AccessToken
}

func (s *ztStack) clientCredentials(sa ztServiceAccount) (int, string) {
	return s.tokenRequest(url.Values{"grant_type": {"client_credentials"}, "client_id": {sa.clientID}, "client_secret": {sa.secret}})
}

func (s *ztStack) mustToken(sa ztServiceAccount) string {
	s.t.Helper()
	status, tok := s.clientCredentials(sa)
	if status != http.StatusOK || tok == "" {
		s.t.Fatalf("client credentials for %s: status %d", sa.clientID, status)
	}
	return tok
}

type ztResponse struct {
	status int
	header http.Header
	body   string
}

func (s *ztStack) ingressGet(subdomain, path string, header http.Header) ztResponse {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.ingress.URL+path, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Host = subdomain + ".localhost"
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		s.t.Fatalf("ingress get %s%s: %v", subdomain, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return ztResponse{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

func ztBearer(tok string) http.Header {
	return http.Header{"X-Mishmesh-Auth": {"Bearer " + tok}}
}

func (s *ztStack) runAgent(opts agent.Options) <-chan error {
	s.t.Helper()
	if opts.GatewayURL == "" {
		opts.GatewayURL = s.wsURL
	}
	opts.Log = s.log
	opts.Out = io.Discard
	cli := agent.New(opts)
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan error, 1)
	go func() { done <- cli.Run(ctx) }()
	exited := make(chan struct{})
	go func() {
		err := <-done
		done <- err
		close(exited)
	}()
	s.t.Cleanup(func() {
		cancel()
		<-exited
	})
	return done
}

func (s *ztStack) waitEndpoint(agentID string, match func(*store.Endpoint) bool) *store.Endpoint {
	s.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		eps, _ := s.data.ListEndpointsByAgent(s.ctx, agentID)
		for _, ep := range eps {
			if _, online := s.conns.ResolveEndpoint(ep.ID); online && match(ep) {
				return ep
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.t.Fatalf("endpoint for agent %s not registered", agentID)
	return nil
}

func (s *ztStack) serveHTTP(u *ztUser, name, subdomain string, policy any, backend http.Handler) *store.Endpoint {
	s.t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		s.t.Fatal(err)
	}
	origin := httptest.NewServer(backend)
	s.t.Cleanup(origin.Close)
	agentID, rawToken := u.createAgent(name)
	s.runAgent(agent.Options{Token: rawToken, Endpoints: []agent.EndpointSpec{{
		Kind: store.KindHTTP, Lifecycle: store.LifecycleReserved, Subdomain: subdomain,
		LocalTarget: mustHost(s.t, origin.URL), Policy: raw,
	}}})
	return s.waitEndpoint(agentID, func(ep *store.Endpoint) bool { return ep.Subdomain == subdomain })
}

func ztEventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type ztRelay struct {
	ln      net.Listener
	target  string
	mu      sync.Mutex
	blocked bool
	conns   map[net.Conn]struct{}
	wg      sync.WaitGroup
}

func newZTRelay(t *testing.T, target string) *ztRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &ztRelay{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	r.wg.Go(r.accept)
	t.Cleanup(func() {
		_ = ln.Close()
		r.cut()
		r.wg.Wait()
	})
	return r
}

func (r *ztRelay) url() string { return "ws://" + r.ln.Addr().String() }

func (r *ztRelay) accept() {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		blocked := r.blocked
		r.mu.Unlock()
		if blocked {
			_ = c.Close()
			continue
		}
		up, err := net.DialTimeout("tcp", r.target, time.Second)
		if err != nil {
			_ = c.Close()
			continue
		}
		if !r.track(c, up) {
			continue
		}
		r.wg.Go(func() { r.pipe(c, up) })
		r.wg.Go(func() { r.pipe(up, c) })
	}
}

func (r *ztRelay) track(cs ...net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blocked {
		for _, c := range cs {
			_ = c.Close()
		}
		return false
	}
	for _, c := range cs {
		r.conns[c] = struct{}{}
	}
	return true
}

func (r *ztRelay) pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func (r *ztRelay) cut() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blocked = true
	for c := range r.conns {
		_ = c.Close()
		delete(r.conns, c)
	}
}

func (r *ztRelay) restore() {
	r.mu.Lock()
	r.blocked = false
	r.mu.Unlock()
}
