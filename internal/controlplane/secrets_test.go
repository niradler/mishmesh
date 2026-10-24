package controlplane

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/orgsecret"
)

func TestSecretsAPI(t *testing.T) {
	f := newTenantFixture(t)
	if status, _ := f.do(t, f.alice, http.MethodPut, "/api/v1/secrets/k", `{"value":"v"}`); status != http.StatusServiceUnavailable {
		t.Fatalf("without vault: %d", status)
	}
	f.api.SetSecretVault(orgsecret.New(f.api.data, testSecretBox(t)))
	tests := []struct {
		name string
		path string
		body string
		want int
	}{
		{"bad name", "/api/v1/secrets/Bad.Name", `{"value":"v"}`, http.StatusBadRequest},
		{"empty value", "/api/v1/secrets/k", `{"value":""}`, http.StatusBadRequest},
		{"newline value", "/api/v1/secrets/k", `{"value":"a\nb"}`, http.StatusBadRequest},
		{"oversized value", "/api/v1/secrets/k", `{"value":"` + strings.Repeat("x", orgsecret.MaxValueBytes+1) + `"}`, http.StatusBadRequest},
		{"valid", "/api/v1/secrets/k", `{"value":"first-value"}`, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := f.do(t, f.alice, http.MethodPut, tc.path, tc.body)
			if status != tc.want {
				t.Fatalf("status %d want %d: %s", status, tc.want, body)
			}
		})
	}
	var sec orgSecretDTO
	doc(t, f.alice, f.srv, http.MethodPut, "/api/v1/secrets/k", `{"value":"second-value"}`, http.StatusOK, &sec)
	if sec.Version != 2 || !sec.HasValue {
		t.Fatalf("rotated secret = %+v", sec)
	}
	status, body := f.do(t, f.alice, http.MethodGet, "/api/v1/secrets", "")
	if status != http.StatusOK || strings.Contains(body, "value\":\"") || strings.Contains(body, "second-value") {
		t.Fatalf("list: %d %s", status, body)
	}
	doc(t, f.alice, f.srv, http.MethodDelete, "/api/v1/secrets/k", "", http.StatusNoContent, nil)
	doc(t, f.alice, f.srv, http.MethodDelete, "/api/v1/secrets/k", "", http.StatusNotFound, nil)
}
