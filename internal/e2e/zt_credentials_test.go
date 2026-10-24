package e2e

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type ztUpstreamRecorder struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (r *ztUpstreamRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.seen[req.URL.Path] = req.Header.Values("X-Upstream-Key")
	r.mu.Unlock()
	w.Header().Set("X-Upstream-Echo", "ok")
	_, _ = io.WriteString(w, "upstream answered")
}

func (r *ztUpstreamRecorder) keys(path string) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.seen[path]
	return v, ok
}

func TestZeroTrustUpstreamCredentialsByGroup(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@example.com", "")
	const opsSecret, devSecret = "ops-very-secret-value", "dev-very-secret-value"
	owner.putSecret("ops-key", opsSecret)
	owner.putSecret("dev-key", devSecret)

	upstream := &ztUpstreamRecorder{seen: map[string][]string{}}
	ep := s.serveHTTP(owner, "billing-host", "billing", map[string]any{"identity": map[string]any{
		"required": true,
		"credentials": map[string]any{
			"ops":   map[string]string{"header": "X-Upstream-Key", "secret": "ops-key", "prefix": "Key "},
			"dev":   map[string]string{"header": "X-Upstream-Key", "secret": "dev-key"},
			"ghost": map[string]string{"header": "X-Upstream-Key", "secret": "never-stored"},
		},
	}}, upstream)

	opsGroup, devGroup, ghostGroup := owner.createGroup("ops"), owner.createGroup("dev"), owner.createGroup("ghost")
	opsSA, devSA, ghostSA, plainSA := owner.createServiceAccount("ops-bot"), owner.createServiceAccount("dev-bot"), owner.createServiceAccount("ghost-bot"), owner.createServiceAccount("plain-bot")
	owner.addGroupMember(opsGroup, store.GroupMemberServiceAccount, opsSA.id)
	owner.addGroupMember(devGroup, store.GroupMemberServiceAccount, devSA.id)
	owner.addGroupMember(ghostGroup, store.GroupMemberServiceAccount, ghostSA.id)
	var policy strings.Builder
	for name, group := range map[string]string{"ops": opsGroup, "dev": devGroup, "ghost": ghostGroup} {
		policy.WriteString(`@id("zt-cred-` + name + `")
@credential("` + name + `")
permit (principal in Group::"` + group + `", action in [Action::"endpoint:access", Action::"http:request"], resource);
`)
	}
	owner.putPolicy(policy.String())

	cases := []struct {
		name     string
		sa       ztServiceAccount
		path     string
		wantKeys []string
	}{
		{"ops group gets ops credential", opsSA, "/ops", []string{"Key " + opsSecret}},
		{"dev group gets dev credential", devSA, "/dev", []string{devSecret}},
		{"no group gets no credential", plainSA, "/plain", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := ztBearer(s.mustToken(tc.sa))
			hdr.Set("X-Upstream-Key", "forged-by-client")
			got := s.ingressGet("billing", tc.path, hdr)
			if got.status != http.StatusOK || got.body != "upstream answered" {
				t.Fatalf("status %d body %q", got.status, got.body)
			}
			keys, ok := upstream.keys(tc.path)
			if !ok {
				t.Fatal("upstream never saw the request")
			}
			if strings.Join(keys, "|") != strings.Join(tc.wantKeys, "|") {
				t.Fatalf("upstream got X-Upstream-Key %q, want %q", keys, tc.wantKeys)
			}
			for _, secret := range []string{opsSecret, devSecret} {
				if strings.Contains(got.body, secret) {
					t.Fatal("secret leaked in response body")
				}
				for k, vs := range got.header {
					for _, v := range vs {
						if strings.Contains(v, secret) {
							t.Fatalf("secret leaked in response header %s", k)
						}
					}
				}
			}
		})
	}

	t.Run("missing secret fails closed", func(t *testing.T) {
		got := s.ingressGet("billing", "/ghost", ztBearer(s.mustToken(ghostSA)))
		if got.status != http.StatusBadGateway || !strings.Contains(got.body, "upstream credential unavailable") {
			t.Fatalf("status %d body %q, want 502 upstream credential unavailable", got.status, got.body)
		}
		if _, hit := upstream.keys("/ghost"); hit {
			t.Fatal("request reached upstream without its credential")
		}
		var events []ztAccessEvent
		ztEventually(t, 5*time.Second, "credential failure access event", func() bool {
			events = owner.accessLog(url.Values{"principal": {ghostSA.id}, "endpoint": {ep.ID}})
			return len(events) > 0
		})
		if ev := events[0]; ev.Decision != store.AccessDeny || ev.Reason != "credential_unavailable" || ev.Status != http.StatusBadGateway {
			t.Fatalf("access event %+v, want deny credential_unavailable 502", ev)
		}
	})
	t.Run("rotated secret is injected on the very next request", func(t *testing.T) {
		const rotated = "ops-rotated-value"
		owner.putSecret("ops-key", rotated)
		got := s.ingressGet("billing", "/ops-rotated", ztBearer(s.mustToken(opsSA)))
		if got.status != http.StatusOK {
			t.Fatalf("status %d body %q", got.status, got.body)
		}
		if keys, _ := upstream.keys("/ops-rotated"); strings.Join(keys, "|") != "Key "+rotated {
			t.Fatalf("upstream got %q after rotation, want the rotated value", keys)
		}
	})

	t.Run("deleted secret fails closed on the very next request", func(t *testing.T) {
		if status, raw := owner.do(http.MethodDelete, "/api/v1/secrets/dev-key", nil); status != http.StatusNoContent {
			t.Fatalf("delete secret: status %d: %s", status, raw)
		}
		got := s.ingressGet("billing", "/dev-deleted", ztBearer(s.mustToken(devSA)))
		if got.status != http.StatusBadGateway {
			t.Fatalf("status %d body %q, want 502 after the secret was deleted", got.status, got.body)
		}
		if _, hit := upstream.keys("/dev-deleted"); hit {
			t.Fatal("request reached upstream after its secret was deleted")
		}
	})
}
