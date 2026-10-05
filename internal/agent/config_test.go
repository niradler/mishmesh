package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func mapLookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

const validConfig = `
gateway: wss://connect.example.com
token: ${MISHMESH_TOKEN}
allow: [ "10.0.0.0/8:443" ]
tunnels:
  web:
    proto: http
    addr: 3000
    subdomain: app
    policy:
      request_headers_add: { X-From: mishmesh }
      force_https: true
  db:
    proto: tcp
    addr: db.internal:5432
    port: 10005
  api:
    proto: tls
    addr: 8443
    domain: api.example.com
`

func TestParseConfigValid(t *testing.T) {
	cfg, err := ParseConfig("test.yml", []byte(validConfig), mapLookup(map[string]string{"MISHMESH_TOKEN": "mm_secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway != "wss://connect.example.com" || cfg.Token != "mm_secret" {
		t.Fatalf("gateway/token = %q/%q", cfg.Gateway, cfg.Token)
	}
	if len(cfg.Allow) != 1 || cfg.Allow[0] != "10.0.0.0/8:443" {
		t.Fatalf("allow = %v", cfg.Allow)
	}
	if got := strings.Join(cfg.TunnelNames(), ","); got != "web,db,api" {
		t.Fatalf("tunnel order = %s", got)
	}

	specs, err := cfg.Specs(nil)
	if err != nil {
		t.Fatal(err)
	}
	web, db, api := specs[0], specs[1], specs[2]
	if web.Kind != store.KindHTTP || web.LocalTarget != "127.0.0.1:3000" || web.Subdomain != "app" || web.Lifecycle != store.LifecycleReserved {
		t.Fatalf("web = %+v", web)
	}
	if !strings.Contains(string(web.Policy), `"force_https":true`) || !strings.Contains(string(web.Policy), "X-From") {
		t.Fatalf("web policy = %s", web.Policy)
	}
	if db.Kind != store.KindTCP || db.LocalTarget != "db.internal:5432" || db.Port != 10005 {
		t.Fatalf("db = %+v", db)
	}
	if api.Kind != store.KindTLS || api.Domain != "api.example.com" || api.TargetTLS {
		t.Fatalf("api = %+v", api)
	}
}

func TestEnvExpansion(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		env       map[string]string
		wantToken string
		wantPort  int
		wantErr   string
	}{
		{name: "plain var", yaml: "token: ${T}", env: map[string]string{"T": "abc"}, wantToken: "abc"},
		{name: "default used when unset", yaml: "token: ${T:-fallback}", wantToken: "fallback"},
		{name: "default used when empty", yaml: "token: ${T:-fallback}", env: map[string]string{"T": ""}, wantToken: "fallback"},
		{name: "env wins over default", yaml: "token: ${T:-fallback}", env: map[string]string{"T": "real"}, wantToken: "real"},
		{name: "embedded in string", yaml: `token: "pre-${T}-post"`, env: map[string]string{"T": "x"}, wantToken: "pre-x-post"},
		{name: "unset is an error", yaml: "token: ${MISSING}", wantErr: "environment variable MISSING is not set"},
		{name: "empty without default is an error", yaml: "token: ${T}", env: map[string]string{"T": ""}, wantErr: "environment variable T is not set"},
		{name: "numeric field from env", yaml: "token: x\n__PORT__", env: map[string]string{"P": "10005"}, wantToken: "x", wantPort: 10005},
		{name: "dollar without braces untouched", yaml: "token: pa$$word", wantToken: "pa$$word"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.yaml
			tunnels := "tunnels:\n  t:\n    proto: tcp\n    addr: 1234\n"
			if strings.Contains(body, "__PORT__") {
				body = strings.Replace(body, "__PORT__", "", 1)
				tunnels += "    port: ${P}\n"
			}
			cfg, err := ParseConfig("t.yml", []byte(body+"\n"+tunnels), mapLookup(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Token != tt.wantToken {
				t.Fatalf("token = %q, want %q", cfg.Token, tt.wantToken)
			}
			if tt.wantPort != 0 && cfg.Tunnels[0].Port != tt.wantPort {
				t.Fatalf("port = %d, want %d", cfg.Tunnels[0].Port, tt.wantPort)
			}
		})
	}
}

func TestParseConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{name: "not yaml", yaml: "tunnels: [", want: []string{"cannot parse YAML"}},
		{name: "top level list", yaml: "- a", want: []string{"top level must be a mapping"}},
		{name: "no tunnels", yaml: "gateway: ws://x:1", want: []string{"at least one tunnel is required"}},
		{name: "empty tunnels", yaml: "tunnels: {}", want: []string{"at least one tunnel is required"}},
		{name: "tunnels as list", yaml: "tunnels: [a]", want: []string{"tunnels must be a mapping"}},
		{name: "bad gateway scheme", yaml: "gateway: ftp://x\ntunnels:\n  a: {proto: tcp, addr: 1}", want: []string{"unsupported scheme"}},
		{name: "unknown root field", yaml: "gatway: ws://x\ntunnels:\n  a: {proto: tcp, addr: 1}", want: []string{`unknown field "gatway"`}},
		{name: "unknown tunnel field", yaml: "tunnels:\n  a: {proto: tcp, addr: 1, prot: 2}", want: []string{`unknown field "prot" in tunnel "a"`}},
		{name: "bad proto", yaml: "tunnels:\n  a: {proto: udp, addr: 1}", want: []string{`proto must be one of http, tcp, tls, got "udp"`}},
		{name: "missing addr", yaml: "tunnels:\n  a: {proto: http}", want: []string{"addr is required"}},
		{name: "bad tunnel name", yaml: "tunnels:\n  \"bad name\": {proto: tcp, addr: 1}", want: []string{`tunnel name "bad name" is invalid`}},
		{name: "duplicate tunnel", yaml: "tunnels:\n  a: {proto: tcp, addr: 1}\n  a: {proto: tcp, addr: 2}", want: []string{`tunnel "a" is defined twice`}},
		{name: "subdomain and domain", yaml: "tunnels:\n  a: {proto: http, addr: 1, subdomain: x, domain: y.com}", want: []string{"mutually exclusive"}},
		{name: "port on http", yaml: "tunnels:\n  a: {proto: http, addr: 1, port: 5}", want: []string{"port only applies to tcp"}},
		{name: "subdomain on tcp", yaml: "tunnels:\n  a: {proto: tcp, addr: 1, subdomain: x}", want: []string{"do not apply to tcp"}},
		{name: "tls without host", yaml: "tunnels:\n  a: {proto: tls, addr: 1}", want: []string{"need a subdomain or domain"}},
		{name: "tls target_https", yaml: "tunnels:\n  a: {proto: tls, addr: 1, subdomain: x, target_https: true}", want: []string{"target_https does not apply to tls"}},
		{name: "port out of range", yaml: "tunnels:\n  a: {proto: tcp, addr: 1, port: 70000}", want: []string{"out of range"}},
		{name: "bad policy key", yaml: "tunnels:\n  a: {proto: http, addr: 1, policy: {nope: 1}}", want: []string{"policy:", "nope"}},
		{name: "port not a number", yaml: "tunnels:\n  a: {proto: tcp, addr: 1, port: abc}", want: []string{`tunnel "a"`}},
		{
			name: "several problems reported together",
			yaml: "tunnels:\n  a: {proto: udp, addr: 1}\n  b: {proto: http}",
			want: []string{"proto must be one of", "addr is required"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig("t.yml", []byte(tt.yaml), mapLookup(nil))
			if err == nil {
				t.Fatal("expected an error")
			}
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("error type = %T", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err.Error(), w)
				}
			}
		})
	}
}

func TestParseConfigErrorsCarryLineNumbers(t *testing.T) {
	_, err := ParseConfig("t.yml", []byte("gateway: ws://x:1\ntunnels:\n  a:\n    proto: udp\n    addr: 1\n"), mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "line 3:") {
		t.Fatalf("err = %v, want line 3", err)
	}
}

func TestSpecsSelection(t *testing.T) {
	cfg, err := ParseConfig("t.yml", []byte(validConfig), mapLookup(map[string]string{"MISHMESH_TOKEN": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		selected []string
		want     []string
		wantErr  string
	}{
		{name: "all", want: []string{"web", "db", "api"}},
		{name: "subset keeps requested order", selected: []string{"db", "web"}, want: []string{"db", "web"}},
		{name: "duplicates collapse", selected: []string{"web", "web"}, want: []string{"web"}},
		{name: "unknown", selected: []string{"nope"}, wantErr: `unknown tunnel "nope" (configured: web, db, api)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs, err := cfg.Specs(tt.selected)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range specs {
				got = append(got, s.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSpecLifecycleAndTLSTarget(t *testing.T) {
	tests := []struct {
		name      string
		tunnel    TunnelConfig
		wantLife  string
		wantTLS   bool
		wantAddr  string
		wantKind  string
		wantReady bool
	}{
		{name: "plain http ephemeral", tunnel: TunnelConfig{Proto: "http", Addr: "3000"}, wantLife: store.LifecycleEphemeral, wantAddr: "127.0.0.1:3000"},
		{name: "reserved flag", tunnel: TunnelConfig{Proto: "http", Addr: "3000", Reserved: true}, wantLife: store.LifecycleReserved, wantAddr: "127.0.0.1:3000"},
		{name: "https scheme sets target tls", tunnel: TunnelConfig{Proto: "http", Addr: "https://svc:443"}, wantLife: store.LifecycleEphemeral, wantTLS: true, wantAddr: "svc:443"},
		{name: "tls proto never wraps target", tunnel: TunnelConfig{Proto: "tls", Addr: "tls://svc:8443", Subdomain: "x"}, wantLife: store.LifecycleReserved, wantTLS: false, wantAddr: "svc:8443"},
		{name: "tcp port reserves", tunnel: TunnelConfig{Proto: "tcp", Addr: ":22", Port: 10022}, wantLife: store.LifecycleReserved, wantAddr: "127.0.0.1:22"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := NamedTunnel{Name: "n", TunnelConfig: tt.tunnel}.Spec()
			if err != nil {
				t.Fatal(err)
			}
			if spec.Lifecycle != tt.wantLife || spec.TargetTLS != tt.wantTLS || spec.LocalTarget != tt.wantAddr {
				t.Fatalf("spec = %+v", spec)
			}
		})
	}
}

func TestFindConfig(t *testing.T) {
	dir := t.TempDir()
	first := dir + "/a.yml"
	second := dir + "/b.yml"
	if err := writeFile(second); err != nil {
		t.Fatal(err)
	}
	got, err := FindConfig([]string{first, second})
	if err != nil || got != second {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := FindConfig([]string{first}); err == nil || !strings.Contains(err.Error(), "no config file found") {
		t.Fatalf("err = %v", err)
	}
}
