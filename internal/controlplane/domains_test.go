package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeResolver struct {
	mu      sync.Mutex
	records map[string][]string
}

func (f *fakeResolver) set(name string, values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[name] = values
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.records[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

type domainFixture struct {
	srv      *httptest.Server
	data     store.DataStore
	resolver *fakeResolver
	agentA   string
}

func newDomainFixture(t *testing.T, verification bool) *domainFixture {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	ctx := context.Background()
	for _, id := range []string{"org_a", "org_b"} {
		if err := data.CreateOrg(ctx, &store.Org{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	agent := &store.Agent{ID: "ag_a", OrgID: "org_a", Name: "a", Status: store.AgentActive, CreatedAt: time.Now()}
	if err := data.CreateAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeResolver{records: map[string][]string{}}
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetPublicConfig("mishmesh.io:8080", "https")
	api.SetDomainVerification(verification, resolver)
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &domainFixture{srv: srv, data: data, resolver: resolver, agentA: agent.ID}
}

func TestDomainVerificationFlow(t *testing.T) {
	f := newDomainFixture(t, true)

	var created domainDTO
	do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_a", `{"name":"App.Example.com."}`, http.StatusCreated, &created)
	if created.Name != "app.example.com" || created.Verified || created.Challenge == nil {
		t.Fatalf("created: %+v", created)
	}
	if created.Challenge.Name != "_mishmesh-challenge.app.example.com" || created.Challenge.Type != "TXT" || created.CNAMETarget != "mishmesh.io" {
		t.Fatalf("challenge: %+v", created)
	}

	var again domainDTO
	do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_a", `{"name":"app.example.com"}`, http.StatusOK, &again)
	if again.ID != created.ID || again.Challenge.Value != created.Challenge.Value {
		t.Fatalf("register must be idempotent: %+v vs %+v", again, created)
	}

	bindBody := `{"agent_id":"ag_a","kind":"http","domain":"app.example.com"}`
	do(t, f.srv, http.MethodPost, "/api/v1/endpoints?org_id=org_a", bindBody, http.StatusForbidden, nil)

	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+created.ID+"/verify?org_id=org_a", "", http.StatusUnprocessableEntity, nil)

	f.resolver.set(challengePrefix+"app.example.com", "something-else")
	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+created.ID+"/verify?org_id=org_a", "", http.StatusUnprocessableEntity, nil)

	f.resolver.set(challengePrefix+"app.example.com", "other", created.Challenge.Value)
	var verified domainDTO
	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+created.ID+"/verify?org_id=org_a", "", http.StatusOK, &verified)
	if !verified.Verified || verified.Challenge != nil {
		t.Fatalf("verified: %+v", verified)
	}

	var ep endpointDTO
	do(t, f.srv, http.MethodPost, "/api/v1/endpoints?org_id=org_a", bindBody, http.StatusCreated, &ep)
	if ep.Domain != "app.example.com" || ep.PublicURL != "https://app.example.com" {
		t.Fatalf("endpoint: %+v", ep)
	}

	do(t, f.srv, http.MethodDelete, "/api/v1/domains/"+created.ID+"?org_id=org_a", "", http.StatusConflict, nil)
	do(t, f.srv, http.MethodDelete, "/api/v1/endpoints/"+ep.ID+"?org_id=org_a", "", http.StatusNoContent, nil)
	do(t, f.srv, http.MethodDelete, "/api/v1/domains/"+created.ID+"?org_id=org_a", "", http.StatusNoContent, nil)
}

func TestDomainCrossOrgIsolation(t *testing.T) {
	f := newDomainFixture(t, true)

	var a domainDTO
	do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_a", `{"name":"shop.example.com"}`, http.StatusCreated, &a)
	f.resolver.set(challengePrefix+"shop.example.com", a.Challenge.Value)
	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+a.ID+"/verify?org_id=org_a", "", http.StatusOK, nil)

	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+a.ID+"/verify?org_id=org_b", "", http.StatusNotFound, nil)
	do(t, f.srv, http.MethodDelete, "/api/v1/domains/"+a.ID+"?org_id=org_b", "", http.StatusNotFound, nil)

	var listB []domainDTO
	do(t, f.srv, http.MethodGet, "/api/v1/domains?org_id=org_b", "", http.StatusOK, &listB)
	if len(listB) != 0 {
		t.Fatalf("org_b must not see org_a domains: %+v", listB)
	}

	do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_b", `{"name":"shop.example.com"}`, http.StatusConflict, nil)

	if err := f.data.CreateAgent(context.Background(), &store.Agent{ID: "ag_b", OrgID: "org_b", Name: "b", Status: store.AgentActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	do(t, f.srv, http.MethodPost, "/api/v1/endpoints?org_id=org_b", `{"agent_id":"ag_b","kind":"http","domain":"shop.example.com"}`, http.StatusForbidden, nil)
}

func TestUnverifiedClaimCannotBeVerifiedWithVictimsToken(t *testing.T) {
	f := newDomainFixture(t, true)

	var a, b domainDTO
	do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_a", `{"name":"victim.example.com"}`, http.StatusCreated, &a)
	do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_b", `{"name":"victim.example.com"}`, http.StatusCreated, &b)
	if a.Challenge.Value == b.Challenge.Value {
		t.Fatal("challenge tokens must be per org")
	}
	f.resolver.set(challengePrefix+"victim.example.com", a.Challenge.Value)
	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+b.ID+"/verify?org_id=org_b", "", http.StatusUnprocessableEntity, nil)
	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+a.ID+"/verify?org_id=org_a", "", http.StatusOK, nil)
	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+b.ID+"/verify?org_id=org_b", "", http.StatusConflict, nil)
}

func TestDomainNameValidation(t *testing.T) {
	f := newDomainFixture(t, true)
	for _, name := range []string{"", "localhost", "10.0.0.1", "mishmesh.io", "x.mishmesh.io", "bad_name.example.com", "-a.example.com", "a..example.com"} {
		do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_a", `{"name":"`+name+`"}`, http.StatusBadRequest, nil)
	}
}

func TestEndpointDomainWithoutVerification(t *testing.T) {
	f := newDomainFixture(t, false)

	do(t, f.srv, http.MethodGet, "/api/v1/domains?org_id=org_a", "", http.StatusNotFound, nil)

	var ep endpointDTO
	do(t, f.srv, http.MethodPost, "/api/v1/endpoints?org_id=org_a", `{"agent_id":"ag_a","kind":"http","domain":"Home.Example.com"}`, http.StatusCreated, &ep)
	if ep.Domain != "home.example.com" {
		t.Fatalf("domain must be normalized: %+v", ep)
	}
	do(t, f.srv, http.MethodPost, "/api/v1/endpoints?org_id=org_a", `{"agent_id":"ag_a","kind":"http","domain":"x.mishmesh.io"}`, http.StatusBadRequest, nil)
}

func TestPatchEndpointDomainRequiresVerification(t *testing.T) {
	f := newDomainFixture(t, true)

	var ep endpointDTO
	do(t, f.srv, http.MethodPost, "/api/v1/endpoints?org_id=org_a", `{"agent_id":"ag_a","kind":"http"}`, http.StatusCreated, &ep)
	do(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID+"?org_id=org_a", `{"domain":"nope.example.com"}`, http.StatusForbidden, nil)

	var d domainDTO
	do(t, f.srv, http.MethodPost, "/api/v1/domains?org_id=org_a", `{"name":"ok.example.com"}`, http.StatusCreated, &d)
	f.resolver.set(challengePrefix+"ok.example.com", d.Challenge.Value)
	do(t, f.srv, http.MethodPost, "/api/v1/domains/"+d.ID+"/verify?org_id=org_a", "", http.StatusOK, nil)
	do(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID+"?org_id=org_a", `{"domain":"ok.example.com"}`, http.StatusOK, &ep)
	if ep.Domain != "ok.example.com" {
		t.Fatalf("patched: %+v", ep)
	}
	do(t, f.srv, http.MethodPatch, "/api/v1/endpoints/"+ep.ID+"?org_id=org_a", `{"domain":""}`, http.StatusOK, &ep)
	if ep.Domain != "" {
		t.Fatalf("clearing domain: %+v", ep)
	}
}
