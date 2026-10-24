package controlplane

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/egressca"
	"github.com/mishmesh/mishmesh/internal/orgsecret"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/workload"
)

type tenantFixture struct {
	srv        *httptest.Server
	api        *API
	alice      *http.Client
	bob        *http.Client
	orgA       string
	orgB       string
	userA      string
	agentA     string
	tokenA     string
	endpoint   string
	inviteA    string
	inviteTk   string
	service    string
	connection string
	group      string
	sa         string
	wid        string
}

func newTenantFixture(t *testing.T) *tenantFixture {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "tenant.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "admin-secret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("localhost:8080", "http")
	api.SetReachInEnabled(true)
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := &tenantFixture{srv: srv, api: api}
	var a, b meBody
	f.alice, a = register(t, srv, "alice@example.com", http.StatusCreated)
	f.bob, b = register(t, srv, "bob@example.com", http.StatusCreated)
	f.orgA, f.orgB, f.userA = a.ActiveOrgID, b.ActiveOrgID, a.ID

	var created struct {
		Agent agentDTO `json:"agent"`
		Token string   `json:"token"`
	}
	doc(t, f.alice, srv, http.MethodPost, "/api/v1/agents", `{"name":"alice-agent"}`, http.StatusCreated, &created)
	f.agentA, f.tokenA = created.Agent.ID, created.Token
	var ep endpointDTO
	doc(t, f.alice, srv, http.MethodPost, "/api/v1/endpoints", `{"agent_id":"`+f.agentA+`","subdomain":"alice-sub"}`, http.StatusCreated, &ep)
	f.endpoint = ep.ID
	inv := invite(t, f.alice, srv, "dave@example.com", "member")
	f.inviteA, f.inviteTk = inv.ID, inv.InviteToken
	return f
}

func (f *tenantFixture) fill(pattern string) (string, string) {
	method, path, _ := strings.Cut(pattern, " ")
	r := strings.NewReplacer(
		"/agents/{id}", "/agents/"+f.agentA,
		"/endpoints/{id}", "/endpoints/"+f.endpoint,
		"/orgs/{id}", "/orgs/"+f.orgA,
		"/invites/{id}", "/invites/"+f.inviteA,
		"{user_id}", f.userA,
		"{agent_id}", f.agentA,
		"/services/{id}", "/services/"+f.service,
		"/connections/{id}", "/connections/"+f.connection,
		"/groups/{id}", "/groups/"+f.group,
		"/service-accounts/{id}", "/service-accounts/"+f.sa,
		"{wid}", f.wid,
		"{type}", "user",
		"{member_id}", f.userA,
		"{name}", "shared-key",
		"{hostname}", "alice-lab.example.com",
	)
	return method, r.Replace(path)
}

func (f *tenantFixture) do(t *testing.T, c *http.Client, method, path, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestCrossTenantEveryRoute(t *testing.T) {
	f := newTenantFixture(t)
	f.api.SetServices(newFakeServices())
	f.api.SetConnections(newFakeConnectionControl(), testConnectionRegistry(t), testSecretBox(t))
	f.api.SetHostCommands(true)
	f.api.SetSecretVault(orgsecret.New(f.api.data, testSecretBox(t)))
	f.api.SetEgressCA(egressca.New(f.api.data, testSecretBox(t)))
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/egress/decrypt", `{"allow":["alice-decrypt.example"]}`, http.StatusOK, nil)
	var aliceCA egressCADTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/egress/ca", "", http.StatusOK, &aliceCA)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/secrets/shared-key", `{"value":"alice-secret-value"}`, http.StatusOK, nil)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/egress/credentials/shared-key", `{"header":"X-Alice-Egress","secret":"shared-key","agent_ids":["`+f.agentA+`"]}`, http.StatusOK, nil)
	var svc serviceDTO
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/services", `{"name":"alice-svc","driver":"docker","config":{"containers":["alice-c"]}}`, http.StatusCreated, &svc)
	f.service = svc.ID
	var connection struct {
		ID string `json:"id"`
	}
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/connections", `{"type":"third","name":"alice-lab","config":{},"secrets":{"third_key":"alice-credential"}}`, http.StatusCreated, &connection)
	f.connection = connection.ID
	var grp groupDTO
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/groups", `{"name":"alice-grp"}`, http.StatusCreated, &grp)
	f.group = grp.ID
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/groups/"+f.group+"/members", `{"type":"user","id":"`+f.userA+`"}`, http.StatusNoContent, nil)
	var sa struct {
		ServiceAccount serviceAccountDTO `json:"service_account"`
	}
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/service-accounts", `{"name":"alice-robot"}`, http.StatusCreated, &sa)
	f.sa = sa.ServiceAccount.ID
	f.api.SetWorkloadVerifier(workload.NewVerifier(workload.Options{}))
	var wid workloadIdentityDTO
	doc(t, f.alice, f.srv, http.MethodPost, "/api/v1/service-accounts/"+f.sa+"/workload-identities", `{"issuer":"https://issuer.example","audience":"mm","subject":"repo:wl-subject-a"}`, http.StatusCreated, &wid)
	f.wid = wid.ID
	if err := f.api.data.AppendAccessEvents(context.Background(), []*store.AccessEvent{{
		ID: "acc_alice", OrgID: f.orgA, At: time.Now().UTC(), Source: store.AccessSourceIngress, Decision: store.AccessAllow,
		PrincipalType: "User", PrincipalID: f.userA, EndpointID: f.endpoint, Path: "/alice-access-path",
	}}); err != nil {
		t.Fatal(err)
	}

	type probe struct {
		body string
		want int
	}
	notFound := http.StatusNotFound
	probes := map[string]probe{
		"GET /api/v1/orgs/{id}":                                          {"", notFound},
		"PATCH /api/v1/members/{user_id}":                                {`{"role":"admin"}`, notFound},
		"DELETE /api/v1/members/{user_id}":                               {"", notFound},
		"POST /api/v1/agents":                                            {`{"name":"x","org_id":"` + f.orgA + `"}`, notFound},
		"GET /api/v1/agents/{id}":                                        {"", notFound},
		"PATCH /api/v1/agents/{id}":                                      {`{"name":"hijacked"}`, notFound},
		"DELETE /api/v1/agents/{id}":                                     {"", notFound},
		"POST /api/v1/agents/{id}/rotate":                                {"", notFound},
		"POST /api/v1/agents/{id}/revoke":                                {"", notFound},
		"GET /api/v1/agents/{id}/endpoints":                              {"", notFound},
		"GET /api/v1/agents/{id}/tokens":                                 {"", notFound},
		"POST /api/v1/endpoints":                                         {`{"agent_id":"` + f.agentA + `","subdomain":"bob-sub"}`, notFound},
		"GET /api/v1/endpoints/{id}":                                     {"", notFound},
		"PATCH /api/v1/endpoints/{id}":                                   {`{"subdomain":"pwned"}`, notFound},
		"DELETE /api/v1/endpoints/{id}":                                  {"", notFound},
		"POST /api/v1/reach/{agent_id}/http":                             {`{"target":"127.0.0.1:1"}`, notFound},
		"GET /api/v1/reach/{agent_id}/stream":                            {"", notFound},
		"GET /api/v1/invites":                                            {"", http.StatusOK},
		"DELETE /api/v1/invites/{id}":                                    {"", notFound},
		"GET /api/v1/agents":                                             {"", http.StatusOK},
		"GET /api/v1/endpoints":                                          {"", http.StatusOK},
		"GET /api/v1/members":                                            {"", http.StatusOK},
		"GET /api/v1/groups":                                             {"", http.StatusOK},
		"POST /api/v1/groups":                                            {`{"name":"bob-grp"}`, http.StatusCreated},
		"GET /api/v1/groups/{id}":                                        {"", notFound},
		"PATCH /api/v1/groups/{id}":                                      {`{"name":"pwned"}`, notFound},
		"DELETE /api/v1/groups/{id}":                                     {"", notFound},
		"POST /api/v1/groups/{id}/members":                               {`{"type":"user","id":"` + f.userA + `"}`, notFound},
		"DELETE /api/v1/groups/{id}/members/{type}/{member_id}":          {"", notFound},
		"GET /api/v1/service-accounts":                                   {"", http.StatusOK},
		"POST /api/v1/service-accounts":                                  {`{"name":"bob-robot"}`, http.StatusCreated},
		"GET /api/v1/service-accounts/{id}":                              {"", notFound},
		"PATCH /api/v1/service-accounts/{id}":                            {`{"disabled":true}`, notFound},
		"DELETE /api/v1/service-accounts/{id}":                           {"", notFound},
		"POST /api/v1/service-accounts/{id}/rotate":                      {"", notFound},
		"GET /api/v1/service-accounts/{id}/workload-identities":          {"", notFound},
		"POST /api/v1/service-accounts/{id}/workload-identities":         {`{"issuer":"https://issuer.example","audience":"mm","subject":"repo:bob"}`, notFound},
		"DELETE /api/v1/service-accounts/{id}/workload-identities/{wid}": {"", notFound},
		"POST /api/v1/members":                                           {`{"email":"carol@example.com","role":"member"}`, http.StatusCreated},
		"GET /api/v1/orgs":                                               {"", http.StatusOK},
		"POST /api/v1/orgs":                                              {`{"name":"bobs-second"}`, http.StatusCreated},
		"GET /api/v1/quota":                                              {"", http.StatusOK},
		"PUT /api/v1/quota":                                              {`{"max_agents":1,"max_endpoints":1,"max_bandwidth_bytes":1}`, http.StatusForbidden},
		"GET /api/v1/audit":                                              {"", http.StatusOK},
		"GET /api/v1/access-log":                                         {"", http.StatusOK},
		"GET /api/v1/status":                                             {"", http.StatusOK},
		"GET /api/v1/policy":                                             {"", http.StatusOK},
		"GET /api/v1/services":                                           {"", http.StatusOK},
		"GET /api/v1/service-drivers":                                    {"", http.StatusOK},
		"POST /api/v1/services":                                          {`{"name":"bob-svc","driver":"docker","config":{"containers":["bob"]}}`, http.StatusCreated},
		"GET /api/v1/services/{id}":                                      {"", notFound},
		"PUT /api/v1/services/{id}":                                      {`{"name":"pwned","driver":"docker","config":{"containers":["x"]}}`, notFound},
		"DELETE /api/v1/services/{id}":                                   {"", notFound},
		"POST /api/v1/services/{id}/start":                               {"", notFound},
		"POST /api/v1/services/{id}/stop":                                {"", notFound},
		"GET /api/v1/capabilities":                                       {"", http.StatusOK},
		"GET /api/v1/connection-types":                                   {"", http.StatusOK},
		"GET /api/v1/connections":                                        {"", http.StatusOK},
		"POST /api/v1/connections":                                       {`{"type":"third","name":"bob-connection","config":{},"secrets":{}}`, http.StatusCreated},
		"GET /api/v1/connections/{id}":                                   {"", notFound},
		"PUT /api/v1/connections/{id}":                                   {`{"name":"pwned","config":{}}`, notFound},
		"DELETE /api/v1/connections/{id}":                                {"", notFound},
		"POST /api/v1/connections/{id}/sync":                             {"", notFound},
		"POST /api/v1/connections/{id}/hostnames":                        {`{"hostname":"pwned.example.com","service":"http://localhost:1"}`, notFound},
		"DELETE /api/v1/connections/{id}/hostnames/{hostname}":           {"", notFound},
		"POST /api/v1/connections/detect":                                {`{"type":"third","input":{}}`, http.StatusOK},
		"GET /api/v1/topology":                                           {"", http.StatusOK},
		"GET /api/v1/secrets":                                            {"", http.StatusOK},
		"PUT /api/v1/secrets/{name}":                                     {`{"value":"bob-value"}`, http.StatusOK},
		"DELETE /api/v1/secrets/{name}":                                  {"", http.StatusNoContent},
		"GET /api/v1/egress/credentials":                                 {"", http.StatusOK},
		"GET /api/v1/egress/credentials/{name}":                          {"", notFound},
		"PUT /api/v1/egress/credentials/{name}":                          {`{"header":"X-Bob","secret":"bob-key","agent_ids":[]}`, http.StatusOK},
		"DELETE /api/v1/egress/credentials/{name}":                       {"", http.StatusNoContent},
		"GET /api/v1/egress/ca":                                          {"", http.StatusOK},
		"GET /api/v1/egress/decrypt":                                     {"", http.StatusOK},
		"PUT /api/v1/egress/decrypt":                                     {`{"allow":["bob.example"]}`, http.StatusOK},
		"PUT /api/v1/policy":                                             {`{"matrix":{"owner":["agent:read","agent:write","endpoint:read","endpoint:write","quota:read","quota:write","member:read","member:manage","audit:read","status:read","policy:read","policy:write"],"member":["agent:read"]}}`, http.StatusOK},
	}

	leaks := []string{f.agentA, f.endpoint, f.orgA, f.userA, f.tokenA, f.inviteA, f.inviteTk, "dave@example.com", "alice", "alice-sub", "alice-agent", "alice-svc", "alice-lab", "alicetun", "aliceacct", f.service, f.connection, f.group, "alice-credential", f.sa, "alice-robot", "/alice-access-path", "acc_alice", "alice-secret-value", "X-Alice-Egress", "alice-decrypt.example", aliceCA.SHA256, aliceCA.PEM[30:90], f.wid, "wl-subject-a"}

	for _, rt := range f.api.routes() {
		pr, ok := probes[rt.pattern]
		if !ok {
			t.Errorf("route %q has no cross-tenant probe", rt.pattern)
			continue
		}
		t.Run(rt.pattern, func(t *testing.T) {
			method, path := f.fill(rt.pattern)
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			status, body := f.do(t, f.bob, method, path+sep+"org_id="+f.orgA, pr.body)
			if status != pr.want {
				t.Fatalf("status %d want %d: %s", status, pr.want, body)
			}
			for _, secret := range leaks {
				if strings.Contains(body, secret) {
					t.Fatalf("response leaks %q: %s", secret, body)
				}
			}
		})
	}

	var aliceDecrypt egressDecryptDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/egress/decrypt", "", http.StatusOK, &aliceDecrypt)
	if len(aliceDecrypt.Allow) != 1 || aliceDecrypt.Allow[0] != "alice-decrypt.example" {
		t.Fatalf("alice decrypt config tampered: %+v", aliceDecrypt)
	}
	var agents []agentDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/agents", "", http.StatusOK, &agents)
	if len(agents) != 1 || agents[0].Name != "alice-agent" || agents[0].Status != "active" {
		t.Fatalf("alice agent tampered: %+v", agents)
	}
	var toks []tokenDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/agents/"+f.agentA+"/tokens", "", http.StatusOK, &toks)
	if len(toks) != 1 || toks[0].RevokedAt != nil {
		t.Fatalf("alice tokens tampered: %+v", toks)
	}
	var q quotaDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/quota", "", http.StatusOK, &q)
	if q.MaxAgents == 1 {
		t.Fatal("bob changed alice's quota")
	}
	var eps []endpointDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/endpoints", "", http.StatusOK, &eps)
	if len(eps) != 1 || eps[0].Subdomain != "alice-sub" {
		t.Fatalf("alice endpoints tampered: %+v", eps)
	}
	var members []memberDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/members", "", http.StatusOK, &members)
	if len(members) != 1 {
		t.Fatalf("alice members tampered: %+v", members)
	}
	var invites []inviteDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/invites", "", http.StatusOK, &invites)
	if len(invites) != 1 || invites[0].ID != f.inviteA {
		t.Fatalf("alice invites tampered: %+v", invites)
	}
	var secretList []orgSecretDTO
	status, body := f.do(t, f.alice, http.MethodGet, "/api/v1/secrets", "")
	if status != http.StatusOK || strings.Contains(body, "alice-secret-value") {
		t.Fatalf("alice secrets list: %d %s", status, body)
	}
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/secrets", "", http.StatusOK, &secretList)
	if len(secretList) != 1 || secretList[0].Name != "shared-key" || secretList[0].Version != 1 || !secretList[0].HasValue {
		t.Fatalf("alice secrets tampered: %+v", secretList)
	}
	var wids []workloadIdentityDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/service-accounts/"+f.sa+"/workload-identities", "", http.StatusOK, &wids)
	if len(wids) != 1 || wids[0].ID != f.wid || wids[0].Subject != "repo:wl-subject-a" {
		t.Fatalf("alice workload identities tampered: %+v", wids)
	}
}

func TestAdminTokenCanTargetAnyOrg(t *testing.T) {
	f := newTenantFixture(t)
	admin := &http.Client{Transport: bearerTransport{token: "admin-secret"}}

	var agents []agentDTO
	doc(t, admin, f.srv, http.MethodGet, "/api/v1/agents?org_id="+f.orgA, "", http.StatusOK, &agents)
	if len(agents) != 1 || agents[0].ID != f.agentA {
		t.Fatalf("admin list: %+v", agents)
	}
	doc(t, admin, f.srv, http.MethodGet, "/api/v1/agents/"+f.agentA, "", http.StatusOK, nil)
	var created struct {
		Agent agentDTO `json:"agent"`
	}
	doc(t, admin, f.srv, http.MethodPost, "/api/v1/agents", `{"name":"by-admin","org_id":"`+f.orgB+`"}`, http.StatusCreated, &created)
	if created.Agent.OrgID != f.orgB {
		t.Fatalf("admin create org: %+v", created.Agent)
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	f := newTenantFixture(t)
	status, body := f.do(t, f.bob, http.MethodGet, "/api/v1/nope", "")
	if status != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Fatalf("got %d %s", status, body)
	}
}

const allActionsPolicy = `{"matrix":{"owner":["agent:read","agent:write","endpoint:read","endpoint:write","quota:read","quota:write","member:read","member:manage","audit:read","status:read","policy:read","policy:write"]}}`

func TestTenantOwnerCannotWriteQuota(t *testing.T) {
	f := newTenantFixture(t)

	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/quota", `{"max_agents":0,"max_endpoints":0,"max_bandwidth_bytes":0}`, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/policy", allActionsPolicy, http.StatusOK, nil)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/quota", `{"max_agents":0,"max_endpoints":0,"max_bandwidth_bytes":0}`, http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/quota", "", http.StatusOK, nil)

	admin := &http.Client{Transport: bearerTransport{token: "admin-secret"}}
	doc(t, admin, f.srv, http.MethodPut, "/api/v1/quota?org_id="+f.orgA, `{"max_agents":7,"max_endpoints":8,"max_bandwidth_bytes":9}`, http.StatusOK, nil)
	var q quotaDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/quota", "", http.StatusOK, &q)
	if q.MaxAgents != 7 || q.MaxEndpoints != 8 || q.MaxBandwidthBytes != 9 {
		t.Fatalf("operator quota write not applied: %+v", q)
	}
}

func TestOperatorBypassesHostileOrgPolicy(t *testing.T) {
	f := newTenantFixture(t)
	admin := &http.Client{Transport: bearerTransport{token: "admin-secret"}}

	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/policy", `{"matrix":{"member":["agent:read"]}}`, http.StatusOK, nil)
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/agents", "", http.StatusForbidden, nil)
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/policy", allActionsPolicy, http.StatusForbidden, nil)

	q := "?org_id=" + f.orgA
	doc(t, admin, f.srv, http.MethodGet, "/api/v1/agents"+q, "", http.StatusOK, nil)
	doc(t, admin, f.srv, http.MethodPost, "/api/v1/agents"+q, `{"name":"op-agent"}`, http.StatusCreated, nil)
	doc(t, admin, f.srv, http.MethodPut, "/api/v1/quota"+q, `{"max_agents":5}`, http.StatusOK, nil)
	doc(t, admin, f.srv, http.MethodPut, "/api/v1/policy"+q, allActionsPolicy, http.StatusOK, nil)
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/agents", "", http.StatusOK, nil)
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
