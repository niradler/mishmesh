package controlplane

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/workload"
	"github.com/mishmesh/mishmesh/internal/workload/workloadtest"
)

const workloadAudience = "mishmesh-test"

func (f *saFixture) trustWorkload(t *testing.T, org, saID, issuer, subject string) workloadIdentityDTO {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"issuer": issuer, "audience": workloadAudience, "subject": subject})
	code, out := f.call(t, http.MethodPost, "/api/v1/service-accounts/"+saID+"/workload-identities?org_id="+org, saAdminToken, string(body))
	if code != http.StatusCreated {
		t.Fatalf("trust workload: %d %s", code, out)
	}
	var dto workloadIdentityDTO
	if err := json.Unmarshal(out, &dto); err != nil {
		t.Fatal(err)
	}
	return dto
}

func assertionForm(raw string) url.Values {
	return url.Values{"grant_type": {"client_credentials"}, "client_assertion_type": {jwtBearerAssertionType}, "client_assertion": {raw}}
}

func newWorkloadFixture(t *testing.T) (*saFixture, *workloadtest.Issuer) {
	f := newSAFixture(t)
	f.api.SetWorkloadVerifier(workload.NewVerifier(workload.Options{AllowHTTP: true}))
	return f, workloadtest.NewIssuer(t)
}

func TestWorkloadIdentityExchange(t *testing.T) {
	f, iss := newWorkloadFixture(t)
	admin := f.createSA(t, "org_a", "ci", "admin")
	f.trustWorkload(t, "org_a", admin.ServiceAccount.ID, iss.URL, "repo:org/app:ref:main")

	raw := iss.Sign(t, iss.Default("repo:org/app:ref:main", workloadAudience))
	code, out, hdr := f.tokenRequest(t, assertionForm(raw), "", "")
	if code != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange: %d %v", code, out)
	}
	tok := out["access_token"].(string)
	if code, body := f.call(t, http.MethodPost, "/api/v1/reach/ag_a/http", tok, reachBody); code != http.StatusBadGateway {
		t.Fatalf("workload token reach: %d %s", code, body)
	}

	if code, out, _ := f.tokenRequest(t, assertionForm(raw), "", ""); code != http.StatusUnauthorized || out["error"] != oauthInvalidClient {
		t.Fatalf("replayed assertion: %d %v", code, out)
	}

	second := iss.Sign(t, iss.Default("repo:org/app:ref:main", workloadAudience))
	form := assertionForm(second)
	form.Set("client_id", admin.ServiceAccount.ClientID)
	if code, out, _ := f.tokenRequest(t, form, "", ""); code != http.StatusOK {
		t.Fatalf("matching client_id: %d %v", code, out)
	}
	if code, _ := f.call(t, http.MethodPost, "/api/v1/reach/ag_a/http", tok, reachBody); code != http.StatusBadGateway {
		t.Fatalf("earlier workload token revoked by a later exchange: %d", code)
	}
}

func TestWorkloadIdentityExchangeRejects(t *testing.T) {
	f, iss := newWorkloadFixture(t)
	admin := f.createSA(t, "org_a", "ci", "admin")
	other := f.createSA(t, "org_a", "other", "member")
	f.trustWorkload(t, "org_a", admin.ServiceAccount.ID, iss.URL, "repo:org/app")
	untrusted := workloadtest.NewIssuer(t)

	cases := []struct {
		name string
		form func() url.Values
		code int
	}{
		{"wrong audience", func() url.Values {
			return assertionForm(iss.Sign(t, iss.Default("repo:org/app", "someone-else")))
		}, http.StatusUnauthorized},
		{"wrong subject", func() url.Values {
			return assertionForm(iss.Sign(t, iss.Default("repo:org/evil", workloadAudience)))
		}, http.StatusUnauthorized},
		{"untrusted issuer", func() url.Values {
			return assertionForm(untrusted.Sign(t, untrusted.Default("repo:org/app", workloadAudience)))
		}, http.StatusUnauthorized},
		{"expired", func() url.Values {
			a := iss.Default("repo:org/app", workloadAudience)
			a.IssuedAt, a.Expiry = time.Now().Add(-time.Hour), time.Now().Add(-10*time.Minute)
			return assertionForm(iss.Sign(t, a))
		}, http.StatusUnauthorized},
		{"lifetime too long", func() url.Values {
			a := iss.Default("repo:org/app", workloadAudience)
			a.Expiry = a.IssuedAt.Add(3 * time.Hour)
			return assertionForm(iss.Sign(t, a))
		}, http.StatusUnauthorized},
		{"mismatched client_id", func() url.Values {
			form := assertionForm(iss.Sign(t, iss.Default("repo:org/app", workloadAudience)))
			form.Set("client_id", other.ServiceAccount.ClientID)
			return form
		}, http.StatusUnauthorized},
		{"with client secret", func() url.Values {
			form := assertionForm(iss.Sign(t, iss.Default("repo:org/app", workloadAudience)))
			form.Set("client_secret", "x")
			return form
		}, http.StatusBadRequest},
		{"wrong assertion type", func() url.Values {
			form := assertionForm(iss.Sign(t, iss.Default("repo:org/app", workloadAudience)))
			form.Set("client_assertion_type", "urn:other")
			return form
		}, http.StatusBadRequest},
		{"garbage assertion", func() url.Values { return assertionForm("a.b.c") }, http.StatusUnauthorized},
		{"oversize assertion", func() url.Values {
			return assertionForm(strings.Repeat("a", workload.MaxAssertionBytes+1))
		}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := f.tokenRequest(t, tc.form(), "", "")
			if code != tc.code || out["access_token"] != nil {
				t.Fatalf("code = %d %v, want %d", code, out, tc.code)
			}
		})
	}
	if hits := untrusted.JWKSHits.Load(); hits != 0 {
		t.Fatalf("untrusted issuer keys fetched %d times", hits)
	}
}

func TestWorkloadIdentityAmbiguousMatchRejected(t *testing.T) {
	f, iss := newWorkloadFixture(t)
	one := f.createSA(t, "org_a", "one", "admin")
	two := f.createSA(t, "org_b", "two", "admin")
	f.trustWorkload(t, "org_a", one.ServiceAccount.ID, iss.URL, "repo:shared")
	f.trustWorkload(t, "org_b", two.ServiceAccount.ID, iss.URL, "repo:shared")
	code, out, _ := f.tokenRequest(t, assertionForm(iss.Sign(t, iss.Default("repo:shared", workloadAudience))), "", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("ambiguous match: %d %v", code, out)
	}
}

func TestWorkloadIdentityDisabledServiceAccount(t *testing.T) {
	f, iss := newWorkloadFixture(t)
	sa := f.createSA(t, "org_a", "ci", "admin")
	f.trustWorkload(t, "org_a", sa.ServiceAccount.ID, iss.URL, "repo:x")
	if code, body := f.call(t, http.MethodPatch, "/api/v1/service-accounts/"+sa.ServiceAccount.ID+"?org_id=org_a", saAdminToken, `{"disabled":true}`); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, body)
	}
	if code, out, _ := f.tokenRequest(t, assertionForm(iss.Sign(t, iss.Default("repo:x", workloadAudience))), "", ""); code != http.StatusUnauthorized {
		t.Fatalf("disabled sa exchange: %d %v", code, out)
	}
}

func TestWorkloadIdentityIssuerUnavailable(t *testing.T) {
	f, iss := newWorkloadFixture(t)
	sa := f.createSA(t, "org_a", "ci", "admin")
	f.trustWorkload(t, "org_a", sa.ServiceAccount.ID, iss.URL, "repo:x")
	iss.SetDiscoveredIssuer("https://elsewhere.example")
	if code, out, _ := f.tokenRequest(t, assertionForm(iss.Sign(t, iss.Default("repo:x", workloadAudience))), "", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable issuer: %d %v", code, out)
	}
}

func TestWorkloadIdentityNotConfigured(t *testing.T) {
	f := newSAFixture(t)
	iss := workloadtest.NewIssuer(t)
	if code, _, _ := f.tokenRequest(t, assertionForm(iss.Sign(t, iss.Default("s", workloadAudience))), "", ""); code != http.StatusBadRequest {
		t.Fatalf("assertion without verifier = %d, want 400", code)
	}
	sa := f.createSA(t, "org_a", "ci", "admin")
	if code, _ := f.call(t, http.MethodPost, "/api/v1/service-accounts/"+sa.ServiceAccount.ID+"/workload-identities?org_id=org_a", saAdminToken, `{"issuer":"https://x.example","audience":"a","subject":"s"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("create without verifier = %d, want 503", code)
	}
}

func TestWorkloadIdentityAPI(t *testing.T) {
	f := newSAFixture(t)
	f.api.SetWorkloadVerifier(workload.NewVerifier(workload.Options{}))
	sa := f.createSA(t, "org_a", "ci", "admin")
	base := "/api/v1/service-accounts/" + sa.ServiceAccount.ID + "/workload-identities?org_id=org_a"
	bad := []string{
		`{"issuer":"http://plain.example","audience":"a","subject":"s"}`,
		`{"issuer":"https://x.example/","audience":"a","subject":"s"}`,
		`{"issuer":"https://x.example?q=1","audience":"a","subject":"s"}`,
		`{"issuer":"https://u:p@x.example","audience":"a","subject":"s"}`,
		`{"issuer":"https://x.example","audience":"","subject":"s"}`,
		`{"issuer":"https://x.example","audience":"a","subject":"line\nbreak"}`,
		`{"issuer":"https://x.example","audience":"a","subject":"` + strings.Repeat("s", maxWorkloadSubject+1) + `"}`,
	}
	for _, body := range bad {
		if code, out := f.call(t, http.MethodPost, base, saAdminToken, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, code, out)
		}
	}
	good := `{"issuer":"https://x.example","audience":"a","subject":"s"}`
	code, out := f.call(t, http.MethodPost, base, saAdminToken, good)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, out)
	}
	var created workloadIdentityDTO
	_ = json.Unmarshal(out, &created)
	if code, _ := f.call(t, http.MethodPost, base, saAdminToken, good); code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", code)
	}
	if code, _ := f.call(t, http.MethodPost, "/api/v1/service-accounts/sa_missing/workload-identities?org_id=org_a", saAdminToken, good); code != http.StatusNotFound {
		t.Fatalf("missing sa = %d, want 404", code)
	}
	code, out = f.call(t, http.MethodGet, base, saAdminToken, "")
	var list []workloadIdentityDTO
	if err := json.Unmarshal(out, &list); err != nil || code != http.StatusOK || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, _ := f.call(t, http.MethodDelete, "/api/v1/service-accounts/"+sa.ServiceAccount.ID+"/workload-identities/"+created.ID+"?org_id=org_b", saAdminToken, ""); code != http.StatusNotFound {
		t.Fatalf("cross-org delete = %d, want 404", code)
	}
	del := "/api/v1/service-accounts/" + sa.ServiceAccount.ID + "/workload-identities/" + created.ID + "?org_id=org_a"
	if code, _ := f.call(t, http.MethodDelete, del, saAdminToken, ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := f.call(t, http.MethodDelete, del, saAdminToken, ""); code != http.StatusNotFound {
		t.Fatalf("double delete = %d", code)
	}
}
