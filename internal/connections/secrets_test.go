package connections

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldKeepNumericConfigPrecisionDuringRedaction(t *testing.T) {
	result, err := SanitizeJSON(Config{Secrets: map[string]string{"token": "credential"}}, Descriptor{}, json.RawMessage(`{"number":9007199254740993,"token":"credential","note":"credential"}`))
	if err != nil || !strings.Contains(string(result), "9007199254740993") || strings.Contains(string(result), "credential") {
		t.Fatalf("sanitized=%s error=%v", result, err)
	}
	if _, err := SanitizeJSON(Config{}, Descriptor{}, json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("accepted trailing provider response")
	}
}

func TestShouldPreserveOnlyTrustedDiagnosticProvenanceAndRedactItsValues(t *testing.T) {
	cfg := Config{Secrets: map[string]string{"token": "retry"}}
	trusted := WithHostDiagnostic(Status{Health: "down"}, HostOpenFailed)
	if got := SanitizeStatus(cfg, Descriptor{}, trusted); got.LastError != "open connection failed" {
		t.Fatalf("trusted diagnostic=%+v", got)
	}
	trusted = WithHostDiagnostic(Status{Health: "down"}, HostReloadCanceled)
	if got := SanitizeStatus(cfg, Descriptor{}, trusted); strings.Contains(got.LastError, "retry") || !strings.Contains(got.LastError, "canceled before admission") {
		t.Fatalf("trusted redaction=%+v", got)
	}
	forged := newRedactor(Config{}, nil, nil).status(WithHostDiagnostic(Status{Health: "down"}, HostOpenFailed))
	if forged.hostDiagnostic != "" || SanitizeStatus(Config{}, Descriptor{}, forged).LastError != "connection provider reported an error" {
		t.Fatalf("provider forged host provenance=%+v", forged)
	}
	var payload Status
	if err := json.Unmarshal([]byte(`{"hostDiagnostic":"open connection failed","last_error":"open connection failed"}`), &payload); err != nil {
		t.Fatal(err)
	}
	if got := SanitizeStatus(Config{}, Descriptor{}, payload); got.LastError != "connection provider reported an error" || got.hostDiagnostic != "" {
		t.Fatalf("JSON forged provenance=%+v", got)
	}
	if got := WithHostDiagnostic(Status{}, HostDiagnostic("credential-value")); strings.Contains(got.LastError, "credential-value") {
		t.Fatal("non-allowlisted host text escaped")
	}
	raw, err := json.Marshal(trusted)
	if err != nil || strings.Contains(string(raw), "hostDiagnostic") || strings.Contains(string(raw), "host_diagnostic") {
		t.Fatalf("private provenance exposed=%s %v", raw, err)
	}
}

func TestShouldBindSecretsToConnectionAndOrg(t *testing.T) {
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if got := SecretContext("org-a", "conn-a"); got != "connection:org-a:conn-a" {
		t.Fatalf("AAD=%q", got)
	}
	sealed, err := box.Seal([]byte(`{"api_token":"secret"}`), SecretContext("org-a", "conn-a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, context := range []string{SecretContext("org-b", "conn-a"), SecretContext("org-a", "conn-b"), "org-a:api_token"} {
		if _, err := box.Open(sealed, context); err == nil {
			t.Fatal("foreign context decrypted")
		}
	}
}

func TestShouldClonePrivateStateAtConnectionConfigBoundaries(t *testing.T) {
	record := store.Connection{Config: json.RawMessage(`{}`), State: json.RawMessage(`{"private":true}`)}
	opened, err := openConfig(record, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	record.State[0] = '['
	if string(opened.Record.State) != `{"private":true}` {
		t.Fatal("stored state shares its buffer with provider config")
	}
	cloned := cloneConfig(opened)
	cloned.Record.State[0] = '['
	if string(opened.Record.State) != `{"private":true}` {
		t.Fatal("provider config copies share private state")
	}
}
