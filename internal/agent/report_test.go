package agent

import (
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func TestFormatResults(t *testing.T) {
	results := []TunnelResult{
		{Name: "web", Kind: "http", LocalTarget: "127.0.0.1:3000", Binding: tunnel.EndpointBinding{EndpointID: "ep_1", PublicURL: "https://app.example.com", PathURL: "https://example.com/tunnel/ep_1"}},
		{Name: "db", Kind: "tcp", LocalTarget: "db.internal:5432", Binding: tunnel.EndpointBinding{EndpointID: "ep_2", PublicURL: "tcp://example.com:10005"}},
		{Name: "api", Kind: "tls", LocalTarget: "127.0.0.1:8443", Binding: tunnel.EndpointBinding{Error: `subdomain "api" is already taken`}},
		{Name: "old", Kind: "http", LocalTarget: "127.0.0.1:1", Binding: tunnel.EndpointBinding{}},
	}
	out := FormatResults("wss://connect.example.com", results)
	wants := []string{
		"mishmesh agent connected to wss://connect.example.com",
		"web  http  127.0.0.1:3000  ->  https://app.example.com",
		"endpoint: ep_1",
		"path:     https://example.com/tunnel/ep_1",
		"db   tcp   db.internal:5432  ->  tcp://example.com:10005",
		`api  tls   127.0.0.1:8443  FAILED: subdomain "api" is already taken`,
		"old  http  127.0.0.1:1  FAILED: rejected by the gateway without a reason",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n%s", w, out)
		}
	}
	if strings.Count(out, "path:") != 1 {
		t.Errorf("path line should only appear for the http tunnel\n%s", out)
	}
}
