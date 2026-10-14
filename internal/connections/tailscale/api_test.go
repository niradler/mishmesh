package tailscaleconnection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
)

func TestShouldBoundOAuthAndRedactExternalFailures(t *testing.T) {
	for _, response := range []string{strings.Repeat("secret", 200000), `{"access_token":"secret","expires_in":3600,"token_type":"wrong"}`, `{"access_token":"secret","expires_in":999999,"token_type":"Bearer"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
		api := newAPI(server.URL, nil, "client", "secret")
		_, err := api.accessToken(context.Background())
		server.Close()
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error=%v", err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer server.Close()
	api := newAPI(server.URL, nil, "client", "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := api.accessToken(ctx)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe timeout=%v", err)
	}
	if api.client.Timeout != 15*time.Second {
		t.Fatal("missing HTTP deadline")
	}
}

func TestShouldCacheAndExpireOAuthTokens(t *testing.T) {
	fake := newFakeTailscale(t)
	api := newAPI(fake.server.URL, nil, "client-id", "secret-client-secret")
	for range 2 {
		if _, err := api.accessToken(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if fake.tokens != 1 {
		t.Fatalf("tokens=%d", fake.tokens)
	}
	api.expires = time.Now().Add(-time.Second)
	if _, err := api.accessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.tokens != 2 {
		t.Fatalf("expired token reused: %d", fake.tokens)
	}
}

func TestShouldRefuseOAuthRedirectAndResponseBodyErrors(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	api := newAPI(server.URL, nil, "client-id", "secret-client-secret")
	_, err := api.accessToken(context.Background())
	if err == nil || called || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect followed=%v err=%v", called, err)
	}
}

func TestShouldPreserveAllocatedAddressesWhenUpdatingOwnedServiceTags(t *testing.T) {
	fake, provider, data, config, instance := newFixture(t, "managed")
	if _, err := instance.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	addresses := append([]string(nil), fake.services["svc:notes"].Addrs...)
	instance.Close()
	config.Record.Config = json.RawMessage(strings.Replace(string(config.Record.Config), "tag:apps", "tag:updated", 1))
	if err := data.UpdateConnection(context.Background(), &config.Record); err != nil {
		t.Fatal(err)
	}
	reopened, err := provider.Open(config, connections.Deps{Data: data, Origin: "http://127.0.0.1:8090"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	updated := fake.services["svc:notes"]
	if len(updated.Addrs) != 2 || updated.Addrs[0] != addresses[0] || updated.Addrs[1] != addresses[1] || len(updated.Tags) != 1 || updated.Tags[0] != "tag:updated" {
		t.Fatalf("updated=%+v", updated)
	}
}

func TestShouldRecognizeUpstreamETagError(t *testing.T) {
	fake := newFakeTailscale(t)
	fake.conflicts = 1
	local := fake.local("/fake.sock")
	config, err := local.GetServeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = local.SetServeConfig(context.Background(), config)
	if !isETagConflict(err) || isETagConflict(errors.New("unrelated failure")) {
		t.Fatalf("etag classification=%v", err)
	}
}

func TestShouldPreserveKnownAndFutureServiceFieldsDuringTagUpdates(t *testing.T) {
	raw := []byte(`{"name":"svc:notes","displayName":"Notes","comment":"Owned description","addrs":["100.100.100.42","fd7a:115c:a1e0::42"],"ports":["tcp:443"],"tags":["tag:apps"],"future":{"operator":"preserved"}}`)
	var service Service
	if err := json.Unmarshal(raw, &service); err != nil {
		t.Fatal(err)
	}
	service.Tags = []string{"tag:updated"}
	updated, err := json.Marshal(service)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(raw, &before)
	_ = json.Unmarshal(updated, &after)
	for _, key := range []string{"name", "displayName", "comment", "addrs", "ports", "future"} {
		if string(before[key]) != string(after[key]) {
			t.Fatalf("field %s changed: %s", key, updated)
		}
	}
	if string(after["tags"]) != `["tag:updated"]` {
		t.Fatalf("tags=%s", after["tags"])
	}
}
