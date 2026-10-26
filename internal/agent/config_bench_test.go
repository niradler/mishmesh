package agent

import "testing"

const basicAuthConfig = "tunnels:\n  a:\n    proto: http\n    addr: 1\n    policy:\n      basic_auth_user: alice\n      basic_auth_password: s3cret\n"

func BenchmarkParseAndSpecsBasicAuth(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		cfg, err := ParseConfig("bench.yml", []byte(basicAuthConfig), func(string) (string, bool) { return "", false })
		if err != nil {
			b.Fatal(err)
		}
		if _, err := cfg.Specs(nil); err != nil {
			b.Fatal(err)
		}
	}
}
