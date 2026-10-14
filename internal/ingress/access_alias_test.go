package ingress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldRouteDurableAccessAliasesThroughIdenticalIngressGates(t *testing.T) {
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	waker := &fakeWaker{state: services.StateSleeping, known: true}
	ing := newWakeIngress(t, &store.EndpointPolicy{ServiceID: "svc", BasicAuthUser: "u", BasicAuthHash: string(hash)}, waker)
	ing.endpoints = newTTLCache[*store.Endpoint](time.Minute)
	ing.quotas = newTTLCache[*store.Quota](time.Minute)
	ing.conns.AddAgent(&fakeAgentConn{backend: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host+"|"+r.URL.Path+"|"+r.Header.Get("Authorization"))
	})})
	record := &store.Connection{ID: "private", OrgID: "org_1", Name: "private", Type: "tailscale", Config: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := ing.data.CreateConnection(ctx, record); err != nil {
		t.Fatal(err)
	}
	ep, err := ing.data.GetEndpoint(ctx, "ep_1")
	if err != nil {
		t.Fatal(err)
	}
	ep.Domain = "notes.example.org"
	ep.Policy.Access = []store.AccessRef{{ConnectionID: record.ID, Hostname: "notes"}}
	if err := ing.data.UpdateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	publish := func(host string) {
		t.Helper()
		status := connections.Status{Hostnames: []connections.HostStatus{{Host: host, EndpointID: ep.ID, State: "published", AccessHostname: "notes", AccessHostnameKnown: true, EndpointKind: store.KindHTTP}}}
		if err := connections.RecordAccessAliases(ctx, *record, connections.Descriptor{Role: "access", Protocols: []string{store.KindHTTP}}, &status, connections.Deps{Data: ing.data, OnAliasesChanged: ing.InvalidateCaches}); err != nil {
			t.Fatal(err)
		}
	}
	publish("notes.tailnet.ts.net")
	request := func(host, path string, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
		r.Header.Set("Accept", "application/json")
		if auth {
			r.SetBasicAuth("u", "pw")
		}
		w := httptest.NewRecorder()
		ing.ServeHTTP(w, r)
		return w
	}
	for _, host := range []string{"notes.example.org", "notes.tailnet.ts.net"} {
		if w := request(host, "/", false); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized %s = %d %s", host, w.Code, w.Body.String())
		}
	}
	if len(waker.ensured) != 0 {
		t.Fatal("unauthorized alias woke service")
	}
	for _, host := range []string{"notes.example.org", "notes.tailnet.ts.net"} {
		w := request(host, "/path", true)
		if w.Code != http.StatusOK || w.Body.String() != host+"|/path|" {
			t.Fatalf("alias pipeline %s = %d %q", host, w.Code, w.Body.String())
		}
	}
	if len(waker.ensured) != 1 || waker.acquired != 2 || waker.active != 0 {
		t.Fatalf("activity not shared %+v", waker)
	}
	if ing.conns.Usage("org_1") <= 0 || ing.conns.Usage("other") != 0 {
		t.Fatal("metering did not retain endpoint organization")
	}
	publish("notes.new-tailnet.ts.net")
	if w := request("notes.tailnet.ts.net", "/tunnel/ep_1", true); w.Code != http.StatusNotFound {
		t.Fatalf("cached old alias bypassed invalidation %d", w.Code)
	}
	if w := request("notes.new-tailnet.ts.net", "/", true); w.Code != http.StatusOK {
		t.Fatalf("replacement alias %d", w.Code)
	}
	ep.Policy.Access = nil
	if err := ing.data.UpdateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	ing.InvalidateCaches()
	for _, host := range []string{"notes.new-tailnet.ts.net", "other.tailnet.ts.net"} {
		if w := request(host, "/tunnel/ep_1", true); w.Code != http.StatusNotFound {
			t.Fatalf("unknown host reached endpoint %s %d", host, w.Code)
		}
	}
	if w := request("notes.example.org", "/", true); w.Code != http.StatusOK {
		t.Fatalf("canonical route removed %d", w.Code)
	}
	if err := ing.data.SetQuota(ctx, &store.Quota{OrgID: "org_1", MaxBandwidthBytes: 1}); err != nil {
		t.Fatal(err)
	}
	ing.InvalidateCaches()
	if w := request("notes.example.org", "/", true); w.Code != http.StatusTooManyRequests {
		t.Fatalf("quota gate %d %s", w.Code, w.Body.String())
	}
}
