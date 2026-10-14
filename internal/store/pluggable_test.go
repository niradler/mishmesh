package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestShouldMigrateLegacyService(t *testing.T) {
	for _, test := range []struct{ driver, raw, wantDriver, config, ready string }{
		{"docker", `{"docker":{"containers":["web"]},"ready_tcp":"127.0.0.1:80"}`, "docker", `{"containers":["web"]}`, `{"tcp":"127.0.0.1:80"}`},
		{"compose", `{"compose":{"project":"p","file":"/a.yml","bin":"docker","services":["web"]}}`, "compose", `{"project":"p","file":"/a.yml","bin":"docker","services":["web"]}`, `{}`},
		{"exec", `{"exec":{"start":["a"],"stop":["b"],"env":{"A":"secret"}}}`, "command", `{"start":["a"],"stop":["b"],"env":{"A":"secret"}}`, `{}`},
	} {
		t.Run(test.driver, func(t *testing.T) {
			raw := json.RawMessage(test.raw)
			before := string(raw)
			driver, config, ready, err := MigrateService(test.driver, raw)
			if err != nil {
				t.Fatal(err)
			}
			if driver != test.wantDriver {
				t.Fatalf("driver = %s", driver)
			}
			for _, pair := range [][2]string{{string(config), test.config}, {string(ready), test.ready}} {
				var got, want any
				if err := json.Unmarshal([]byte(pair[0]), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(pair[1]), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %s want %s", pair[0], pair[1])
				}
			}
			if string(raw) != before {
				t.Fatal("migration mutated input")
			}
		})
	}
}

func TestShouldRejectInvalidLegacyService(t *testing.T) {
	for _, test := range []struct{ driver, raw string }{
		{"remote-docker", `{"docker":{}}`},
		{"docker", `{"compose":{"project":"p"}}`},
		{"docker", `{"docker":null}`},
		{"docker", `{"docker":[],"ready_tcp":"x"}`},
		{"docker", `{"docker":{},"exec":{}}`},
		{"docker", `{"docker":{},"unknown":true}`},
		{"docker", `{"docker":{},"docker":{}}`},
		{"docker", `{"docker":{"label":"x","label":"y"}}`},
		{"docker", `{"docker":{}} {}`},
		{"docker", `[]`},
		{"docker", ``},
		{"docker", `{"docker":{},"ready_tcp":null}`},
		{"docker", `{"docker":{"containers":"web"}}`},
		{"compose", `{"compose":{"project":"p","unknown":"ignored"}}`},
		{"exec", `{"exec":{"start":["true"],"stop":[]}}`},
	} {
		if _, _, _, err := MigrateService(test.driver, json.RawMessage(test.raw)); err == nil {
			t.Fatalf("accepted %s %s", test.driver, test.raw)
		}
	}
}
