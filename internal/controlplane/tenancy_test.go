package controlplane

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type tenantFixture struct {
	srv      *httptest.Server
	api      *API
	alice    *http.Client
	bob      *http.Client
	orgA     string
	orgB     string
	userA    string
	agentA   string
	tokenA   string
	endpoint string
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
	return f
}

func (f *tenantFixture) fill(pattern string) (string, string) {
	method, path, _ := strings.Cut(pattern, " ")
	r := strings.NewReplacer(
		"/agents/{id}", "/agents/"+f.agentA,
		"/endpoints/{id}", "/endpoints/"+f.endpoint,
		"/orgs/{id}", "/orgs/"+f.orgA,
		"{user_id}", f.userA,
		"{agent_id}", f.agentA,
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

	type probe struct {
		body string
		want int
	}
	notFound := http.StatusNotFound
	probes := map[string]probe{
		"GET /api/v1/orgs/{id}":              {"", notFound},
		"PATCH /api/v1/members/{user_id}":    {`{"role":"admin"}`, notFound},
		"DELETE /api/v1/members/{user_id}":   {"", notFound},
		"POST /api/v1/agents":                {`{"name":"x","org_id":"` + f.orgA + `"}`, notFound},
		"GET /api/v1/agents/{id}":            {"", notFound},
		"PATCH /api/v1/agents/{id}":          {`{"name":"hijacked"}`, notFound},
		"DELETE /api/v1/agents/{id}":         {"", notFound},
		"POST /api/v1/agents/{id}/rotate":    {"", notFound},
		"POST /api/v1/agents/{id}/revoke":    {"", notFound},
		"GET /api/v1/agents/{id}/endpoints":  {"", notFound},
		"GET /api/v1/agents/{id}/tokens":     {"", notFound},
		"POST /api/v1/endpoints":             {`{"agent_id":"` + f.agentA + `","subdomain":"bob-sub"}`, notFound},
		"GET /api/v1/endpoints/{id}":         {"", notFound},
		"PATCH /api/v1/endpoints/{id}":       {`{"subdomain":"pwned"}`, notFound},
		"DELETE /api/v1/endpoints/{id}":      {"", notFound},
		"POST /api/v1/reach/{agent_id}/http": {`{"target":"127.0.0.1:1"}`, notFound},
		"GET /api/v1/agents":                 {"", http.StatusOK},
		"GET /api/v1/endpoints":              {"", http.StatusOK},
		"GET /api/v1/members":                {"", http.StatusOK},
		"POST /api/v1/members":               {`{"email":"carol@example.com","role":"member"}`, http.StatusCreated},
		"GET /api/v1/orgs":                   {"", http.StatusOK},
		"POST /api/v1/orgs":                  {`{"name":"bobs-second"}`, http.StatusCreated},
		"GET /api/v1/quota":                  {"", http.StatusOK},
		"PUT /api/v1/quota":                  {`{"max_agents":1,"max_endpoints":1,"max_bandwidth_bytes":1}`, http.StatusOK},
		"GET /api/v1/audit":                  {"", http.StatusOK},
		"GET /api/v1/status":                 {"", http.StatusOK},
		"GET /api/v1/policy":                 {"", http.StatusOK},
		"PUT /api/v1/policy":                 {`{"matrix":{"owner":["agent:read","agent:write","endpoint:read","endpoint:write","quota:read","quota:write","member:read","member:manage","audit:read","status:read","policy:read","policy:write"],"member":["agent:read"]}}`, http.StatusOK},
	}

	leaks := []string{f.agentA, f.endpoint, f.orgA, f.userA, f.tokenA, "alice", "alice-sub", "alice-agent"}

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

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
