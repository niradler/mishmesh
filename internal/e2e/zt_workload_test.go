package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/safehttp"
	"github.com/mishmesh/mishmesh/internal/workload/workloadtest"
)

const (
	ztWorkloadAudience = "mishmesh-e2e"
	ztWorkloadSubject  = "repo:example/deployer:ref:refs/heads/main"
)

func ztAssertionForm(raw string) url.Values {
	return url.Values{
		"grant_type":            {"client_credentials"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {raw},
	}
}

func (u *ztUser) trustWorkload(sa ztServiceAccount, issuer string) {
	u.s.t.Helper()
	u.mustJSON(http.MethodPost, "/api/v1/service-accounts/"+sa.id+"/workload-identities", map[string]string{
		"issuer":   issuer,
		"audience": ztWorkloadAudience,
		"subject":  ztWorkloadSubject,
	}, http.StatusCreated, nil)
}

func TestZeroTrustWorkloadIdentityThroughIngress(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@example.com", "")
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "deployed:"+r.URL.Path) })
	s.serveHTTP(owner, "deploy-host", "deploy", map[string]any{"identity": map[string]any{"required": true}}, backend)

	sa := owner.createServiceAccount("deployer")
	issuer := workloadtest.NewIssuer(t)
	owner.trustWorkload(sa, issuer.URL)

	accepted := issuer.Sign(t, issuer.Default(ztWorkloadSubject, ztWorkloadAudience))
	status, tok := s.tokenRequest(ztAssertionForm(accepted))
	if status != http.StatusOK || tok == "" {
		t.Fatalf("workload assertion exchange: status %d", status)
	}
	if got := s.ingressGet("deploy", "/release", ztBearer(tok)); got.status != http.StatusOK || got.body != "deployed:/release" {
		t.Fatalf("workload token through ingress: status %d body %q", got.status, got.body)
	}

	untrusted := workloadtest.NewIssuer(t)
	foreignKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rejections := []struct {
		name      string
		assertion func() string
	}{
		{"wrong audience", func() string {
			return issuer.Sign(t, issuer.Default(ztWorkloadSubject, "someone-else"))
		}},
		{"untrusted issuer", func() string {
			return untrusted.Sign(t, untrusted.Default(ztWorkloadSubject, ztWorkloadAudience))
		}},
		{"trusted issuer claim signed by another key", func() string {
			a := issuer.Default(ztWorkloadSubject, ztWorkloadAudience)
			a.SignWith = foreignKey
			return issuer.Sign(t, a)
		}},
		{"expired", func() string {
			a := issuer.Default(ztWorkloadSubject, ztWorkloadAudience)
			a.IssuedAt = time.Now().Add(-30 * time.Minute)
			a.Expiry = time.Now().Add(-20 * time.Minute)
			return issuer.Sign(t, a)
		}},
		{"replayed", func() string { return accepted }},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			status, tok := s.tokenRequest(ztAssertionForm(tc.assertion()))
			if status != http.StatusUnauthorized || tok != "" {
				t.Fatalf("status %d token issued %v, want 401 and no token", status, tok != "")
			}
		})
	}
}

func TestZeroTrustWorkloadLoopbackIssuerBlockedByDefault(t *testing.T) {
	s := newZTStack(t, safehttp.Client(false, true))
	owner := s.register("owner@example.com", "")
	sa := owner.createServiceAccount("deployer")
	issuer := workloadtest.NewIssuer(t)
	owner.trustWorkload(sa, issuer.URL)
	status, tok := s.tokenRequest(ztAssertionForm(issuer.Sign(t, issuer.Default(ztWorkloadSubject, ztWorkloadAudience))))
	if status != http.StatusServiceUnavailable || tok != "" {
		t.Fatalf("loopback issuer: status %d token issued %v, want 503 and no token", status, tok != "")
	}
	if hits := issuer.JWKSHits.Load(); hits != 0 {
		t.Fatalf("server fetched keys from a loopback issuer %d times", hits)
	}
}
