package controlplane

import (
	"net/http"
	"slices"
	"testing"

	"github.com/mishmesh/mishmesh/internal/egressca"
)

func TestEgressDecryptAndCAAPI(t *testing.T) {
	f := newTenantFixture(t)
	var flushed []string
	f.api.SetDecisionFlusher(func(org string) { flushed = append(flushed, org) })

	if status, _ := f.do(t, f.alice, http.MethodGet, "/api/v1/egress/ca", ""); status != http.StatusServiceUnavailable {
		t.Fatalf("ca without authority = %d", status)
	}
	f.api.SetEgressCA(egressca.New(f.api.data, testSecretBox(t)))

	var empty egressDecryptDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/egress/decrypt", "", http.StatusOK, &empty)
	if empty.Allow == nil || len(empty.Allow) != 0 || len(empty.Bypass) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
	for _, body := range []string{`{"allow":["10.0.0.1"]}`, `{"allow":["a..b"]}`, `{"bypass":["*"]}`, `{"allow":[],"extra":1}`} {
		if status, out := f.do(t, f.alice, http.MethodPut, "/api/v1/egress/decrypt", body); status != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", body, status, out)
		}
	}
	if len(flushed) != 0 {
		t.Fatalf("rejected writes flushed: %v", flushed)
	}
	var put egressDecryptDTO
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/egress/decrypt", `{"allow":["API.example.com."," *.corp.example"],"bypass":["bank.corp.example"]}`, http.StatusOK, &put)
	if !slices.Equal(put.Allow, []string{"*.corp.example", "api.example.com"}) || !slices.Equal(put.Bypass, []string{"bank.corp.example"}) {
		t.Fatalf("put = %+v", put)
	}
	if !slices.Equal(flushed, []string{f.orgA}) {
		t.Fatalf("flushes = %v", flushed)
	}
	var bobView egressDecryptDTO
	doc(t, f.bob, f.srv, http.MethodGet, "/api/v1/egress/decrypt", "", http.StatusOK, &bobView)
	if len(bobView.Allow) != 0 {
		t.Fatalf("bob sees alice's config: %+v", bobView)
	}

	var aliceCA, aliceAgain, bobCA egressCADTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/egress/ca", "", http.StatusOK, &aliceCA)
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/egress/ca", "", http.StatusOK, &aliceAgain)
	doc(t, f.bob, f.srv, http.MethodGet, "/api/v1/egress/ca", "", http.StatusOK, &bobCA)
	if aliceCA.SHA256 == "" || aliceCA.SHA256 != aliceAgain.SHA256 || aliceCA.SHA256 == bobCA.SHA256 || len(aliceCA.PEM) == 0 {
		t.Fatalf("ca alice %s/%s bob %s", aliceCA.SHA256, aliceAgain.SHA256, bobCA.SHA256)
	}
}

func TestEgressDecryptRequiresPolicyWrite(t *testing.T) {
	srv := newAuthAPI(t)
	owner := registerClient(t, srv, "owner@example.com", "owner")
	member := inviteAndRegister(t, srv, owner, "member@example.com", "member")
	doc(t, member, srv, http.MethodPut, "/api/v1/egress/decrypt", `{"allow":["api.example.com"]}`, http.StatusForbidden, nil)
	doc(t, owner, srv, http.MethodPut, "/api/v1/egress/decrypt", `{"allow":["api.example.com"]}`, http.StatusOK, nil)
}
