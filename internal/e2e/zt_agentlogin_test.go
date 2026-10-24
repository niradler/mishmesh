package e2e

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/token"
)

type consentView struct {
	ID          string `json:"id"`
	ClientID    string `json:"client_id"`
	OrgID       string `json:"org_id"`
	RedirectURI string `json:"redirect_uri"`
}

func (u *ztUser) authorizeConsentID(authURL string) string {
	u.s.t.Helper()
	c := *u.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(authURL)
	if err != nil {
		u.s.t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || loc.Path != "/consent" || loc.Query().Get("request") == "" {
		u.s.t.Fatalf("authorize = %d %q, want a redirect to the consent request", resp.StatusCode, resp.Header.Get("Location"))
	}
	if loc.Query().Get("code") != "" {
		u.s.t.Fatal("authorize issued a code before consent")
	}
	return loc.Query().Get("request")
}

func followToLoopback(t *testing.T, target string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatalf("consent redirect %q is not the loopback callback", target)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(target)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestAgentBrowserLoginRequiresConsent(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@example.com", "")
	other := owner.invite("other@example.com")

	cases := []struct {
		name    string
		approve bool
	}{
		{"approved consent issues tokens", true},
		{"denied consent refuses the login", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open := func(authURL string) error {
				id := owner.authorizeConsentID(authURL)
				if status, _ := other.do(http.MethodGet, "/api/v1/auth/consent/"+id, nil); status != http.StatusNotFound {
					t.Fatalf("another user read the consent request: %d", status)
				}
				if status, _ := other.do(http.MethodPost, "/api/v1/auth/consent/"+id, map[string]bool{"approve": true}); status != http.StatusNotFound {
					t.Fatalf("another user decided the consent request: %d", status)
				}
				var view consentView
				owner.mustJSON(http.MethodGet, "/api/v1/auth/consent/"+id, nil, http.StatusOK, &view)
				if view.ID != id || view.ClientID != agent.AgentClientID || view.OrgID != owner.org || !strings.HasPrefix(view.RedirectURI, "http://127.0.0.1:") {
					t.Fatalf("consent view = %+v", view)
				}
				var decided struct {
					RedirectTo string `json:"redirect_to"`
				}
				owner.mustJSON(http.MethodPost, "/api/v1/auth/consent/"+id, map[string]bool{"approve": tc.approve}, http.StatusOK, &decided)
				owner.mustJSON(http.MethodPost, "/api/v1/auth/consent/"+id, map[string]bool{"approve": tc.approve}, http.StatusNotFound, nil)
				followToLoopback(t, decided.RedirectTo)
				return nil
			}
			ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
			defer cancel()
			creds, err := agent.LoginBrowser(ctx, agent.LoginOptions{Server: s.api.URL, Open: open})
			if !tc.approve {
				if err == nil || !strings.Contains(err.Error(), "access_denied") {
					t.Fatalf("denied login = %v, %v", creds, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.identity.Authenticate(ctx, creds.AccessToken, token.TypeAgentUser)
			if err != nil || p.Ref != (authz.EntityRef{Type: authz.EntityUser, ID: owner.id}) || p.OrgID != owner.org {
				t.Fatalf("principal = %+v, %v", p, err)
			}
		})
	}
}

type userCodeWriter struct {
	mu    sync.Mutex
	buf   strings.Builder
	codes chan string
	sent  bool
}

var userCodePattern = regexp.MustCompile(`enter code ([A-Z]{4}-[A-Z]{4})`)

func (w *userCodeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if m := userCodePattern.FindStringSubmatch(w.buf.String()); m != nil && !w.sent {
		w.sent = true
		w.codes <- m[1]
	}
	return len(p), nil
}

func TestAgentDeviceLoginVerification(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@example.com", "")
	member := owner.invite("member@example.com")

	cases := []struct {
		name    string
		user    *ztUser
		approve bool
	}{
		{"approved device gets tokens", owner, true},
		{"denied device stops polling", member, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := &userCodeWriter{codes: make(chan string, 1)}
			type result struct {
				creds *agent.Credentials
				err   error
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			defer cancel()
			go func() {
				creds, err := agent.LoginDevice(ctx, agent.LoginOptions{Server: s.api.URL, Out: out})
				done <- result{creds, err}
			}()
			var code string
			select {
			case code = <-out.codes:
			case <-ctx.Done():
				t.Fatal("device login never printed a user code")
			}
			var view struct {
				UserCode string `json:"user_code"`
				ClientID string `json:"client_id"`
			}
			lower := strings.ToLower(strings.ReplaceAll(code, "-", ""))
			tc.user.mustJSON(http.MethodGet, "/api/v1/auth/device?user_code="+lower, nil, http.StatusOK, &view)
			if view.UserCode != code || view.ClientID != agent.AgentClientID {
				t.Fatalf("device view = %+v", view)
			}
			path := "/api/v1/auth/device/deny"
			if tc.approve {
				path = "/api/v1/auth/device/approve"
			}
			tc.user.mustJSON(http.MethodPost, path, map[string]string{"user_code": code}, http.StatusNoContent, nil)
			tc.user.mustJSON(http.MethodGet, "/api/v1/auth/device?user_code="+code, nil, http.StatusNotFound, nil)
			tc.user.mustJSON(http.MethodPost, "/api/v1/auth/device/approve", map[string]string{"user_code": code}, http.StatusNotFound, nil)

			var res result
			select {
			case res = <-done:
			case <-ctx.Done():
				t.Fatal("device login did not finish")
			}
			if !tc.approve {
				if res.err == nil || !strings.Contains(res.err.Error(), "denied") {
					t.Fatalf("denied device login = %v, %v", res.creds, res.err)
				}
				return
			}
			if res.err != nil {
				t.Fatal(res.err)
			}
			p, err := s.identity.Authenticate(ctx, res.creds.AccessToken, token.TypeAgentUser)
			if err != nil || p.Ref.ID != tc.user.id || p.OrgID != tc.user.org {
				t.Fatalf("device principal = %+v, %v", p, err)
			}
		})
	}
}
