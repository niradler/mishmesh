package e2e

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPErrorPages(t *testing.T) {
	t.Run("unreachable local service is 502 with agent-side reason", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dead := ln.Addr().String()
		_ = ln.Close()

		s := startStack(t, nil, stackOptions{localTarget: dead})
		resp, err := http.DefaultClient.Do(s.request(http.MethodGet, "demo.localhost", "/", nil))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		text := string(body)
		if !strings.Contains(text, "upstream service unreachable on the agent side") || !strings.Contains(text, "connection refused") {
			t.Fatalf("body = %q", text)
		}
		if strings.Contains(text, dead) {
			t.Fatalf("body leaks internal address: %q", text)
		}
	})

	t.Run("no agent session is 503", func(t *testing.T) {
		s := startStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), stackOptions{})
		s.conns.UnbindEndpoint(s.endpoint.ID)
		resp, err := http.DefaultClient.Do(s.request(http.MethodGet, "demo.localhost", "/", nil))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "tunnel offline") {
			t.Fatalf("status=%d body=%q", resp.StatusCode, body)
		}
	})
}
