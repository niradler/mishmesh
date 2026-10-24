package e2e

import (
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestZeroTrustIngressAccessLog(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner-a@example.com", "")
	outsider := s.register("owner-b@example.com", "")
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	identityRequired := map[string]any{"identity": map[string]any{"required": true}}
	ep := s.serveHTTP(owner, "ledger-host", "ledger", identityRequired, backend)
	foreignEP := s.serveHTTP(outsider, "vault-host", "vault", identityRequired, backend)

	allowed := owner.createServiceAccount("reader")
	blocked := owner.createServiceAccount("blocked")
	owner.putPolicy(`@id("zt-block-sa")
forbid (principal == ServiceAccount::"` + blocked.id + `", action in [Action::"endpoint:access", Action::"http:request"], resource);`)
	foreignSA := outsider.createServiceAccount("reader")

	requests := []struct {
		name      string
		subdomain string
		path      string
		header    http.Header
		want      int
	}{
		{"allowed", "ledger", "/allowed", ztBearer(s.mustToken(allowed)), http.StatusOK},
		{"denied by policy", "ledger", "/denied", ztBearer(s.mustToken(blocked)), http.StatusForbidden},
		{"anonymous", "ledger", "/anonymous", nil, http.StatusUnauthorized},
		{"other org allowed", "vault", "/foreign", ztBearer(s.mustToken(foreignSA)), http.StatusOK},
	}
	for _, rq := range requests {
		if got := s.ingressGet(rq.subdomain, rq.path, rq.header); got.status != rq.want {
			t.Fatalf("%s: status %d, want %d", rq.name, got.status, rq.want)
		}
	}

	expect := []struct {
		name          string
		path          string
		decision      string
		status        int
		principalType string
		principalID   string
	}{
		{"allow event", "/allowed", store.AccessAllow, http.StatusOK, string(authz.EntityServiceAccount), allowed.id},
		{"policy deny event", "/denied", store.AccessDeny, http.StatusForbidden, string(authz.EntityServiceAccount), blocked.id},
		{"anonymous deny event", "/anonymous", store.AccessDeny, http.StatusUnauthorized, "", ""},
	}
	byPath := map[string]ztAccessEvent{}
	ztEventually(t, 5*time.Second, "ingress access events", func() bool {
		for _, ev := range owner.accessLog(url.Values{"source": {store.AccessSourceIngress}}) {
			byPath[ev.Path] = ev
		}
		for _, e := range expect {
			if _, ok := byPath[e.path]; !ok {
				return false
			}
		}
		return true
	})
	for _, e := range expect {
		t.Run(e.name, func(t *testing.T) {
			ev := byPath[e.path]
			if ev.Source != store.AccessSourceIngress || ev.Decision != e.decision || ev.Status != e.status || ev.EndpointID != ep.ID {
				t.Fatalf("event %+v, want decision %s status %d endpoint %s", ev, e.decision, e.status, ep.ID)
			}
			if e.principalID != "" && (ev.PrincipalType != e.principalType || ev.PrincipalID != e.principalID) {
				t.Fatalf("event principal %s/%s, want %s/%s", ev.PrincipalType, ev.PrincipalID, e.principalType, e.principalID)
			}
		})
	}

	t.Run("filters", func(t *testing.T) {
		denies := owner.accessLog(url.Values{"decision": {store.AccessDeny}, "endpoint": {ep.ID}})
		if len(denies) < 2 {
			t.Fatalf("deny filter returned %d events, want at least 2", len(denies))
		}
		for _, ev := range denies {
			if ev.Decision != store.AccessDeny {
				t.Fatalf("deny filter returned %+v", ev)
			}
		}
		mine := owner.accessLog(url.Values{"principal": {allowed.id}})
		if len(mine) != 1 || mine[0].Path != "/allowed" {
			t.Fatalf("principal filter returned %+v", mine)
		}
	})

	t.Run("org scoped", func(t *testing.T) {
		var foreign []ztAccessEvent
		ztEventually(t, 5*time.Second, "other org event", func() bool {
			foreign = outsider.accessLog(url.Values{})
			return len(foreign) > 0
		})
		for _, ev := range foreign {
			if ev.EndpointID != foreignEP.ID {
				t.Fatalf("other org sees event for endpoint %s: %+v", ev.EndpointID, ev)
			}
		}
		for _, ev := range owner.accessLog(url.Values{}) {
			if ev.EndpointID == foreignEP.ID {
				t.Fatalf("owner sees other org event: %+v", ev)
			}
		}
		if leaked := outsider.accessLog(url.Values{"endpoint": {ep.ID}}); len(leaked) != 0 {
			t.Fatalf("endpoint filter leaks across orgs: %+v", leaked)
		}
	})
}
