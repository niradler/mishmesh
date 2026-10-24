package e2e

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/token"
)

func (u *ztUser) mustOK(method, path string, body any) []byte {
	u.s.t.Helper()
	status, raw := u.do(method, path, body)
	if status/100 != 2 {
		u.s.t.Fatalf("%s %s: status %d: %s", method, path, status, raw)
	}
	return raw
}

func TestZeroTrustServiceAccountTokenThroughIngress(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner-a@example.com", "")
	outsider := s.register("owner-b@example.com", "")

	var leaked atomic.Bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Mishmesh-Auth") != "" {
			leaked.Store(true)
		}
		_, _ = io.WriteString(w, "protected:"+r.URL.Path)
	})
	s.serveHTTP(owner, "orders-host", "orders", map[string]any{"identity": map[string]any{"required": true}}, backend)

	sa := owner.createServiceAccount("ci")
	tok := s.mustToken(sa)
	if got := s.ingressGet("orders", "/data", ztBearer(tok)); got.status != http.StatusOK || got.body != "protected:/data" {
		t.Fatalf("service account token: status %d body %q", got.status, got.body)
	}
	if leaked.Load() {
		t.Fatal("bearer token forwarded to upstream")
	}

	foreign := s.mustToken(outsider.createServiceAccount("ci"))
	gates := []struct {
		name   string
		header http.Header
		want   int
	}{
		{"no token", nil, http.StatusUnauthorized},
		{"garbage token", ztBearer("not-a-token"), http.StatusUnauthorized},
		{"token from another org", ztBearer(foreign), http.StatusNotFound},
	}
	for _, tc := range gates {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.ingressGet("orders", "/data", tc.header); got.status != tc.want {
				t.Fatalf("status %d, want %d (body %q)", got.status, tc.want, got.body)
			}
		})
	}

	revocations := []struct {
		name          string
		revoke        func(sa ztServiceAccount) ztServiceAccount
		newSecretWork bool
	}{
		{"rotate", func(sa ztServiceAccount) ztServiceAccount {
			var out struct {
				ClientSecret string `json:"client_secret"`
			}
			owner.mustJSON(http.MethodPost, "/api/v1/service-accounts/"+sa.id+"/rotate", map[string]any{}, http.StatusOK, &out)
			return ztServiceAccount{id: sa.id, clientID: sa.clientID, secret: out.ClientSecret}
		}, true},
		{"disable", func(sa ztServiceAccount) ztServiceAccount {
			owner.mustOK(http.MethodPatch, "/api/v1/service-accounts/"+sa.id, map[string]any{"disabled": true})
			return sa
		}, false},
		{"delete", func(sa ztServiceAccount) ztServiceAccount {
			owner.mustOK(http.MethodDelete, "/api/v1/service-accounts/"+sa.id, nil)
			return sa
		}, false},
	}
	for _, tc := range revocations {
		t.Run(tc.name, func(t *testing.T) {
			sa := owner.createServiceAccount("sa-" + tc.name)
			tok := s.mustToken(sa)
			if got := s.ingressGet("orders", "/before", ztBearer(tok)); got.status != http.StatusOK {
				t.Fatalf("before %s: status %d", tc.name, got.status)
			}
			after := tc.revoke(sa)
			if got := s.ingressGet("orders", "/after", ztBearer(tok)); got.status != http.StatusUnauthorized {
				t.Fatalf("token still accepted after %s: status %d", tc.name, got.status)
			}
			if status, _ := s.clientCredentials(sa); status == http.StatusOK {
				t.Fatalf("old credentials still mint tokens after %s", tc.name)
			}
			if !tc.newSecretWork {
				return
			}
			fresh := s.mustToken(after)
			if got := s.ingressGet("orders", "/fresh", ztBearer(fresh)); got.status != http.StatusOK {
				t.Fatalf("rotated secret token: status %d", got.status)
			}
		})
	}
}

func (s *ztStack) browserLogin(u *ztUser, subdomain, path string) *http.Cookie {
	s.t.Helper()
	first := s.ingressGet(subdomain, path, nil)
	loginURL := first.header.Get("Location")
	if first.status != http.StatusFound || !strings.HasPrefix(loginURL, s.api.URL+identity.EndpointLoginPath) {
		s.t.Fatalf("anonymous browser request: status %d location %q", first.status, loginURL)
	}
	status, callback := u.endpointLogin(loginURL)
	if status != http.StatusFound {
		s.t.Fatalf("endpoint login: status %d", status)
	}
	cb, err := url.Parse(callback)
	if err != nil || cb.Host != subdomain+".localhost" || cb.Path != identity.EndpointCallbackPath {
		s.t.Fatalf("endpoint login redirect %q: %v", callback, err)
	}
	done := s.ingressGet(subdomain, cb.RequestURI(), nil)
	if done.status != http.StatusFound || done.header.Get("Location") != path {
		s.t.Fatalf("callback: status %d location %q body %q", done.status, done.header.Get("Location"), done.body)
	}
	for _, c := range (&http.Response{Header: done.header}).Cookies() {
		if c.Name == "mm_ep_session" && c.Value != "" {
			return c
		}
	}
	s.t.Fatal("callback set no endpoint session cookie")
	return nil
}

func (u *ztUser) endpointLogin(loginURL string) (int, string) {
	u.s.t.Helper()
	c := *u.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(loginURL)
	if err != nil {
		u.s.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

func ztCookie(c *http.Cookie) http.Header {
	return http.Header{"Cookie": {c.Name + "=" + c.Value}}
}

func TestZeroTrustBrowserLoginRevocation(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@example.com", "")
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("mm_ep_session"); err == nil {
			http.Error(w, "session cookie leaked", http.StatusTeapot)
			return
		}
		_, _ = io.WriteString(w, "app:"+r.URL.Path)
	})
	s.serveHTTP(owner, "portal-host", "portal", map[string]any{"identity": map[string]any{"required": true, "browser_login": true}}, backend)

	cases := []struct {
		name    string
		email   string
		revoke  func(t *testing.T, member *ztUser, cookie *http.Cookie)
		want    int
		restore func(t *testing.T, member *ztUser)
	}{
		{
			name:  "disable member",
			email: "disabled@example.com",
			revoke: func(t *testing.T, member *ztUser, _ *http.Cookie) {
				owner.mustOK(http.MethodPatch, "/api/v1/members/"+member.id, map[string]any{"disabled": true})
			},
			want: http.StatusFound,
		},
		{
			name:  "delete session",
			email: "session@example.com",
			revoke: func(t *testing.T, _ *ztUser, cookie *http.Cookie) {
				p, err := s.identity.Authenticate(s.ctx, cookie.Value, token.TypeSession)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.identity.RevokeSession(s.ctx, p.SessionID); err != nil {
					t.Fatal(err)
				}
			},
			want: http.StatusFound,
		},
		{
			name:  "policy change",
			email: "policy@example.com",
			revoke: func(t *testing.T, member *ztUser, _ *http.Cookie) {
				owner.putPolicy(`@id("zt-block-member")
forbid (principal == User::"` + member.id + `", action in [Action::"endpoint:access", Action::"http:request"], resource);`)
			},
			want: http.StatusForbidden,
			restore: func(t *testing.T, _ *ztUser) {
				owner.putPolicy("")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			member := owner.invite(tc.email)
			cookie := s.browserLogin(member, "portal", "/home")
			if got := s.ingressGet("portal", "/home", ztCookie(cookie)); got.status != http.StatusOK || got.body != "app:/home" {
				t.Fatalf("logged in request: status %d body %q", got.status, got.body)
			}
			tc.revoke(t, member, cookie)
			got := s.ingressGet("portal", "/home", ztCookie(cookie))
			if got.status != tc.want {
				t.Fatalf("after %s: status %d, want %d (body %q)", tc.name, got.status, tc.want, got.body)
			}
			if tc.want == http.StatusFound && !strings.HasPrefix(got.header.Get("Location"), s.api.URL+identity.EndpointLoginPath) {
				t.Fatalf("after %s: redirected to %q, want endpoint login", tc.name, got.header.Get("Location"))
			}
			if tc.restore == nil {
				return
			}
			tc.restore(t, member)
			if got := s.ingressGet("portal", "/home", ztCookie(cookie)); got.status != http.StatusOK {
				t.Fatalf("after restoring policy the same session should pass without re-login: status %d", got.status)
			}
		})
	}

	t.Run("disabled member cannot log back in", func(t *testing.T) {
		member := owner.invite("relogin@example.com")
		s.browserLogin(member, "portal", "/home")
		owner.mustOK(http.MethodPatch, "/api/v1/members/"+member.id, map[string]any{"disabled": true})
		first := s.ingressGet("portal", "/home", nil)
		status, loc := member.endpointLogin(first.header.Get("Location"))
		if status == http.StatusFound && strings.Contains(loc, identity.EndpointCallbackPath) {
			t.Fatalf("disabled member was issued an endpoint login code: %q", loc)
		}
	})
}
