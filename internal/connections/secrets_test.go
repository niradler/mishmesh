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
