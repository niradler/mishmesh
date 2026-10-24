package workload_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/mishmesh/mishmesh/internal/workload"
	"github.com/mishmesh/mishmesh/internal/workload/workloadtest"
)

func newVerifier() *workload.Verifier {
	return workload.NewVerifier(workload.Options{AllowHTTP: true})
}

func TestVerifyAcceptsValidAssertion(t *testing.T) {
	iss := workloadtest.NewIssuer(t)
	a := iss.Default("repo:org/app", "mishmesh")
	c, err := newVerifier().Verify(context.Background(), iss.Sign(t, a), iss.URL)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != a.Subject || c.ID != a.ID || c.Issuer != iss.URL || len(c.Audience) != 1 || c.Audience[0] != "mishmesh" {
		t.Fatalf("claims = %+v", c)
	}
}

func TestVerifyRejects(t *testing.T) {
	iss := workloadtest.NewIssuer(t)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cases := []struct {
		name   string
		mutate func(a *workloadtest.Assertion)
	}{
		{"wrong issuer claim", func(a *workloadtest.Assertion) { a.Issuer = "https://evil.example" }},
		{"expired", func(a *workloadtest.Assertion) {
			a.IssuedAt, a.Expiry = now.Add(-time.Hour), now.Add(-10*time.Minute)
		}},
		{"issued in future", func(a *workloadtest.Assertion) {
			a.IssuedAt, a.Expiry = now.Add(time.Hour), now.Add(70*time.Minute)
		}},
		{"lifetime over one hour", func(a *workloadtest.Assertion) { a.Expiry = a.IssuedAt.Add(2 * time.Hour) }},
		{"lifetime over one hour without iat", func(a *workloadtest.Assertion) {
			a.IssuedAt, a.Expiry = time.Time{}, now.Add(2*time.Hour)
		}},
		{"missing exp", func(a *workloadtest.Assertion) { a.Expiry = time.Time{} }},
		{"missing sub", func(a *workloadtest.Assertion) { a.Subject = "" }},
		{"missing jti", func(a *workloadtest.Assertion) { a.ID = "" }},
		{"missing aud", func(a *workloadtest.Assertion) { a.Audience = nil }},
		{"wrong key", func(a *workloadtest.Assertion) { a.SignWith = other }},
		{"unknown kid", func(a *workloadtest.Assertion) { a.KeyID = "nope" }},
		{"hmac", func(a *workloadtest.Assertion) {
			a.Algorithm, a.SignWith = jose.HS256, []byte(strings.Repeat("k", 32))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := iss.Default("repo:org/app", "mishmesh")
			tc.mutate(&a)
			_, err := newVerifier().Verify(context.Background(), iss.Sign(t, a), iss.URL)
			if !errors.Is(err, workload.ErrInvalidAssertion) {
				t.Fatalf("err = %v, want invalid assertion", err)
			}
		})
	}
}

func TestVerifyRejectsMalformedAndOversize(t *testing.T) {
	v := newVerifier()
	for _, raw := range []string{"", "not.a.jwt", strings.Repeat("a", workload.MaxAssertionBytes+1)} {
		if _, err := v.Verify(context.Background(), raw, "https://issuer.example"); !errors.Is(err, workload.ErrInvalidAssertion) {
			t.Fatalf("raw len %d: err = %v", len(raw), err)
		}
		if _, err := workload.UnverifiedIssuer(raw); !errors.Is(err, workload.ErrInvalidAssertion) {
			t.Fatalf("unverified issuer len %d: err = %v", len(raw), err)
		}
	}
}

func TestVerifyDiscoveryIssuerMismatchIsUnavailable(t *testing.T) {
	iss := workloadtest.NewIssuer(t)
	iss.SetDiscoveredIssuer("https://someone-else.example")
	_, err := newVerifier().Verify(context.Background(), iss.Sign(t, iss.Default("s", "a")), iss.URL)
	if !errors.Is(err, workload.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestVerifyRequiresHTTPSUnlessAllowed(t *testing.T) {
	iss := workloadtest.NewIssuer(t)
	strict := workload.NewVerifier(workload.Options{})
	_, err := strict.Verify(context.Background(), iss.Sign(t, iss.Default("s", "a")), iss.URL)
	if !errors.Is(err, workload.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable for http issuer", err)
	}
}

func TestVerifyIssuerUnreachableIsUnavailable(t *testing.T) {
	iss := workloadtest.NewIssuer(t)
	raw := iss.Sign(t, iss.Default("s", "a"))
	_, err := newVerifier().Verify(context.Background(), raw, "http://127.0.0.1:1")
	if !errors.Is(err, workload.ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
}

func TestVerifyCachesKeysAndThrottlesUnknownKid(t *testing.T) {
	iss := workloadtest.NewIssuer(t)
	v := newVerifier()
	for range 3 {
		if _, err := v.Verify(context.Background(), iss.Sign(t, iss.Default("s", "a")), iss.URL); err != nil {
			t.Fatal(err)
		}
	}
	if hits := iss.JWKSHits.Load(); hits != 1 {
		t.Fatalf("jwks fetched %d times, want 1", hits)
	}
	for range 3 {
		a := iss.Default("s", "a")
		a.KeyID = "rotated"
		if _, err := v.Verify(context.Background(), iss.Sign(t, a), iss.URL); !errors.Is(err, workload.ErrInvalidAssertion) {
			t.Fatalf("err = %v", err)
		}
	}
	if hits := iss.JWKSHits.Load(); hits != 1 {
		t.Fatalf("unknown kid refetched within backoff: %d fetches", hits)
	}
}

func TestUnverifiedIssuer(t *testing.T) {
	iss := workloadtest.NewIssuer(t)
	got, err := workload.UnverifiedIssuer(iss.Sign(t, iss.Default("s", "a")))
	if err != nil || got != iss.URL {
		t.Fatalf("issuer = %q, %v", got, err)
	}
}
