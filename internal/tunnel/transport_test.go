package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDialSendsExtraHeadersWithoutOverridingAuth(t *testing.T) {
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		conn, err := Accept(w, r)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	extra := http.Header{}
	extra.Set("Cf-Access-Client-Id", "id.access")
	extra.Set("Authorization", "Bearer smuggled")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+AgentConnectPath, DialOptions{Token: "agent-token", Header: extra})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	h := <-got
	if v := h.Get("Cf-Access-Client-Id"); v != "id.access" {
		t.Fatalf("extra header = %q", v)
	}
	if v := h.Get("Authorization"); v != "Bearer agent-token" {
		t.Fatalf("authorization = %q, agent token must win", v)
	}
	if v := extra.Get("Authorization"); v != "Bearer smuggled" {
		t.Fatalf("caller header map was mutated: %q", v)
	}
}
