package typedfields

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestShouldRejectUnknownField(t *testing.T) {
	fields := []Field{{Key: "project", Label: "Project", Kind: "text", Required: true}}
	if err := Validate(fields, json.RawMessage(`{"project":"lab","bin":"sh"}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestShouldValidateFieldKinds(t *testing.T) {
	tests := []struct {
		name    string
		field   Field
		value   string
		wantErr bool
	}{
		{"text", Field{Kind: "text"}, `"lab"`, false},
		{"list", Field{Kind: "list"}, `["sudo","systemctl","start","app"]`, false},
		{"keyvalue", Field{Kind: "keyvalue"}, `{"TOKEN":"secret","EMPTY":""}`, false},
		{"secret", Field{Kind: "secret"}, `"secret"`, false},
		{"select", Field{Kind: "select", Options: []string{"Deployment", "StatefulSet"}}, `"Deployment"`, false},
		{"number", Field{Kind: "number"}, `1.5`, false},
		{"bool", Field{Kind: "bool"}, `true`, false},
		{"connection", Field{Kind: "connection", ConnType: "kubernetes"}, `"cluster-id"`, false},
		{"wrong text", Field{Kind: "text"}, `10`, true},
		{"wrong list", Field{Kind: "list"}, `"argv"`, true},
		{"wrong list member", Field{Kind: "list"}, `["argv",1]`, true},
		{"wrong keyvalue", Field{Kind: "keyvalue"}, `[]`, true},
		{"wrong keyvalue member", Field{Kind: "keyvalue"}, `{"TOKEN":true}`, true},
		{"duplicate keyvalue member", Field{Kind: "keyvalue"}, `{"TOKEN":"one","TOKEN":"two"}`, true},
		{"wrong secret", Field{Kind: "secret"}, `false`, true},
		{"wrong select", Field{Kind: "select", Options: []string{"Deployment"}}, `"Pod"`, true},
		{"wrong select type", Field{Kind: "select", Options: []string{"Deployment"}}, `[]`, true},
		{"wrong number", Field{Kind: "number"}, `"1"`, true},
		{"wrong bool", Field{Kind: "bool"}, `"true"`, true},
		{"wrong connection", Field{Kind: "connection"}, `42`, true},
		{"null", Field{Kind: "text"}, `null`, true},
		{"numeric zero", Field{Kind: "number", Required: true}, `0`, false},
		{"negative number", Field{Kind: "number"}, `-20`, false},
		{"maximum finite number", Field{Kind: "number"}, `1.7976931348623157e308`, false},
		{"overflow number", Field{Kind: "number"}, `1e309`, true},
		{"negative overflow number", Field{Kind: "number"}, `-1e309`, true},
		{"non JSON number", Field{Kind: "number"}, `NaN`, true},
		{"false is present", Field{Kind: "bool", Required: true}, `false`, false},
		{"unknown kind", Field{Kind: "textarea"}, `"content"`, true},
		{"empty optional select", Field{Kind: "select", Options: []string{"Deployment"}}, `""`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.field.Key = "value"
			err := Validate([]Field{test.field}, json.RawMessage(`{"value":`+test.value+`}`))
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", err, test.wantErr)
			}
		})
	}
}

func TestShouldRequireNonemptyValues(t *testing.T) {
	for _, kind := range []string{"text", "list", "keyvalue", "secret", "select", "number", "bool", "connection"} {
		t.Run(kind, func(t *testing.T) {
			field := Field{Key: "value", Kind: kind, Required: true, Options: []string{"one"}}
			if err := Validate([]Field{field}, json.RawMessage(`{}`)); err == nil {
				t.Fatal("missing required field accepted")
			}
			if err := Validate([]Field{field}, json.RawMessage(`{"value":null}`)); err == nil {
				t.Fatal("null required field accepted")
			}
		})
	}
	for _, test := range []struct{ kind, value string }{
		{"text", `""`}, {"text", `"  \t "`}, {"secret", `""`}, {"connection", `""`}, {"select", `""`}, {"list", `[]`}, {"keyvalue", `{}`},
	} {
		t.Run(test.kind+test.value, func(t *testing.T) {
			field := Field{Key: "value", Kind: test.kind, Required: true}
			if err := Validate([]Field{field}, json.RawMessage(`{"value":`+test.value+`}`)); err == nil {
				t.Fatal("empty required field accepted")
			}
		})
	}
}

func TestShouldDecodeOneStrictObject(t *testing.T) {
	for _, raw := range []string{"", "null", "[]", `"text"`, "false", "1", `{} {}`, `{} trailing`, `{"value":"one","value":"two"}`, `{"value":`, `{"value":"one",}`} {
		t.Run(raw, func(t *testing.T) {
			if err := Validate([]Field{{Key: "value", Kind: "text"}}, json.RawMessage(raw)); err == nil {
				t.Fatal("invalid object accepted")
			}
		})
	}
	if err := Validate(nil, json.RawMessage(" \n {} \t ")); err != nil {
		t.Fatalf("whitespace rejected: %v", err)
	}
}

func TestShouldValidateDefaults(t *testing.T) {
	for _, test := range []struct {
		name, kind, value string
		wantErr           bool
	}{
		{"text default", "text", `"lab"`, false},
		{"list default", "list", `["command"]`, false},
		{"keyvalue default", "keyvalue", `{"KEY":"value"}`, false},
		{"secret default", "secret", `"secret"`, false},
		{"select default", "select", `"one"`, false},
		{"number default", "number", `1`, false},
		{"zero default", "number", `0`, false},
		{"false default", "bool", `false`, false},
		{"connection default", "connection", `"id"`, false},
		{"wrong number default", "number", `"one"`, true},
		{"overflow default", "number", `1e309`, true},
		{"null default", "text", `null`, true},
		{"trailing default", "text", `"one" "two"`, true},
		{"empty required default", "list", `[]`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := Field{Key: "value", Kind: test.kind, Required: true, Default: json.RawMessage(test.value), Options: []string{"one"}}
			if err := Validate([]Field{field}, json.RawMessage(`{}`)); (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", err, test.wantErr)
			}
		})
	}
	field := Field{Key: "value", Kind: "text", Required: true, Default: json.RawMessage(`"default"`)}
	if err := Validate([]Field{field}, json.RawMessage(`{"value":""}`)); err == nil {
		t.Fatal("default replaced explicit empty required value")
	}
	field.Default = json.RawMessage(`12`)
	if err := Validate([]Field{field}, json.RawMessage(`{"value":"supplied"}`)); err == nil {
		t.Fatal("invalid unused descriptor default accepted")
	}
}

func TestShouldValidateDescriptorKeysAndKinds(t *testing.T) {
	for _, fields := range [][]Field{
		{{Key: "", Kind: "text"}},
		{{Key: "value", Kind: "text"}, {Key: "value", Kind: "number"}},
		{{Key: "value", Kind: "textarea"}},
		{{Key: "value", Kind: ""}},
		{{Key: "token", Kind: "secret"}, {Key: "has_token", Kind: "bool"}},
	} {
		if err := Validate(fields, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("invalid descriptors accepted: %+v", fields)
		}
	}
}

func TestShouldDetectOnlySetHostCommandFields(t *testing.T) {
	for _, test := range []struct {
		name, kind, value string
		marked, want      bool
	}{
		{"absent", "text", "", true, false},
		{"empty string", "text", `""`, true, false},
		{"whitespace is explicitly set", "text", `" \t "`, true, true},
		{"empty argv", "list", `[]`, true, false},
		{"empty map", "keyvalue", `{}`, true, false},
		{"binary", "text", `"docker"`, true, true},
		{"argv", "list", `["systemctl","start","app"]`, true, true},
		{"explicit blank argv element", "list", `[""]`, true, true},
		{"map", "keyvalue", `{"KEY":""}`, true, true},
		{"false", "bool", `false`, true, true},
		{"zero", "number", `0`, true, true},
		{"unmarked", "text", `"docker"`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(`{}`)
			if test.value != "" {
				raw = json.RawMessage(`{"value":` + test.value + `}`)
			}
			got, err := UsesHostCommands([]Field{{Key: "value", Kind: test.kind, HostCommand: test.marked, Sensitive: true}}, raw)
			if err != nil || got != test.want {
				t.Fatalf("UsesHostCommands() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
	fields := []Field{{Key: "bin", Kind: "text", HostCommand: true, Default: json.RawMessage(`"docker"`)}}
	if got, err := UsesHostCommands(fields, json.RawMessage(`{}`)); err != nil || !got {
		t.Fatalf("host-command default ignored: %v, %v", got, err)
	}
	if got, err := UsesHostCommands(fields, json.RawMessage(`{"bin":""}`)); err != nil || got {
		t.Fatalf("default replaced explicit empty field: %v, %v", got, err)
	}
	if got, err := UsesHostCommands(fields, json.RawMessage(`{"bin":"docker","unknown":true}`)); err == nil || got {
		t.Fatalf("invalid config accepted: %v, %v", got, err)
	}
}

func TestShouldRedactWithoutMutatingInput(t *testing.T) {
	fields := []Field{
		{Key: "token", Kind: "secret"},
		{Key: "missing", Kind: "secret"},
		{Key: "empty", Kind: "secret"},
		{Key: "env", Kind: "keyvalue", Sensitive: true},
		{Key: "private", Kind: "text", Sensitive: true},
		{Key: "public", Kind: "keyvalue"},
		{Key: "replicas", Kind: "number", Default: json.RawMessage(`1`)},
	}
	raw := json.RawMessage(`{"token":"never-return","empty":"","env":{"PASSWORD":"do-not-return","EMPTY":""},"private":"hidden","public":{"NAME":"lab"}}`)
	before := append(json.RawMessage(nil), raw...)
	fieldBytes, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := Redact(fields, raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(redacted, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"has_token": true, "has_missing": false, "has_empty": false,
		"env":     map[string]any{"PASSWORD": "<redacted>", "EMPTY": "<redacted>"},
		"private": "<redacted>", "public": map[string]any{"NAME": "lab"}, "replicas": float64(1),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Redact() = %v, want %v", got, want)
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("caller JSON mutated")
	}
	fieldsAfter, err := json.Marshal(fields)
	if err != nil || !bytes.Equal(fieldBytes, fieldsAfter) {
		t.Fatal("caller descriptors mutated")
	}
	for _, secret := range []string{"never-return", "do-not-return", "hidden"} {
		if strings.Contains(string(redacted), secret) {
			t.Fatalf("secret %q disclosed", secret)
		}
	}
}

func TestShouldNotDiscloseSecretsInErrors(t *testing.T) {
	fields := []Field{{Key: "token", Kind: "secret"}, {Key: "env", Kind: "keyvalue", Sensitive: true}}
	for _, raw := range []string{
		`{"token":{"secret":"do-not-return"}}`,
		`{"env":{"PASSWORD":{"value":"do-not-return"}}}`,
		`{"token":"valid"} do-not-return`,
		`{"token":"valid","do-not-return":"value"}`,
	} {
		redacted, err := Redact(fields, json.RawMessage(raw))
		if err == nil || redacted != nil {
			t.Fatalf("invalid config returned data: %s, %v", redacted, err)
		}
		if strings.Contains(err.Error(), "do-not-return") {
			t.Fatalf("error disclosed secret: %v", err)
		}
	}
}

func TestShouldRejectSecretPresenceFlagCollisions(t *testing.T) {
	fields := []Field{{Key: "token", Kind: "secret"}, {Key: "has_token", Kind: "text"}}
	if redacted, err := Redact(fields, json.RawMessage(`{"token":"secret","has_token":"real-value"}`)); err == nil || redacted != nil {
		t.Fatalf("secret flag overwrote a real field: %s, %v", redacted, err)
	}
}

func TestShouldRedactEffectiveDefaultsAndPreserveNumbers(t *testing.T) {
	fields := []Field{
		{Key: "token", Kind: "secret", Default: json.RawMessage(`"default-secret"`)},
		{Key: "whitespace", Kind: "secret"},
		{Key: "env", Kind: "keyvalue", Sensitive: true, Default: json.RawMessage(`{"TOKEN":"default-env-secret"}`)},
		{Key: "number", Kind: "number"},
		{Key: "enabled", Kind: "bool", Default: json.RawMessage(`false`)},
	}
	raw := json.RawMessage(`{"whitespace":" ","number":9007199254740993}`)
	redacted, err := Redact(fields, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"enabled":false,"env":{"TOKEN":"\u003credacted\u003e"},"has_token":true,"has_whitespace":true,"number":9007199254740993}`
	if string(redacted) != want {
		t.Fatalf("Redact() = %s, want %s", redacted, want)
	}
	if string(fields[0].Default) != `"default-secret"` || string(fields[2].Default) != `{"TOKEN":"default-env-secret"}` {
		t.Fatal("descriptor defaults mutated")
	}
	redacted, err = Redact(nil, json.RawMessage(`{}`))
	if err != nil || string(redacted) != `{}` {
		t.Fatalf("empty redaction = %s, %v", redacted, err)
	}
}

func TestShouldSerializeDescriptorWireContract(t *testing.T) {
	field := Field{
		Key: "value", Label: "Value", Kind: "select", Options: []string{"one"}, ConnType: "kubernetes", Required: true,
		Placeholder: "choose", Help: "help", HostCommand: true, Sensitive: true, Default: json.RawMessage(`"one"`),
	}
	raw, err := json.Marshal(field)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"key":"value","label":"Value","kind":"select","options":["one"],"conn_type":"kubernetes","required":true,"placeholder":"choose","help":"help","host_command":true,"sensitive":true,"default":"one"}`
	if string(raw) != want {
		t.Fatalf("descriptor wire JSON = %s, want %s", raw, want)
	}
	raw, err = json.Marshal(Field{Key: "value", Label: "Value", Kind: "text"})
	if err != nil || string(raw) != `{"key":"value","label":"Value","kind":"text","required":false}` {
		t.Fatalf("optional metadata not omitted: %s, %v", raw, err)
	}
}
