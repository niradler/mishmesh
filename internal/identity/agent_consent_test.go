package identity

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
)

func consentLogin() AgentLogin {
	return AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword, Challenge: challengeFor(testVerifier), RedirectURI: "http://127.0.0.1:9000/cb"}
}

func TestStartConsentValidates(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	cases := []struct {
		name  string
		login func(*AgentLogin)
		state string
	}{
		{"missing state", func(*AgentLogin) {}, ""},
		{"oversized state", func(*AgentLogin) {}, strings.Repeat("s", maxStateBytes+1)},
		{"missing user", func(l *AgentLogin) { l.UserID = "" }, "st"},
		{"plain challenge", func(l *AgentLogin) { l.Challenge = "plain" }, "st"},
		{"remote redirect", func(l *AgentLogin) { l.RedirectURI = "http://10.0.0.1:9000/cb" }, "st"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := consentLogin()
			tc.login(&l)
			if _, err := s.StartConsent(ctx, l, tc.state); err == nil {
				t.Fatal("invalid consent request accepted")
			}
		})
	}
}

func TestConsentDecisions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newClockedService(t, func() time.Time { return now })
	ctx := context.Background()

	t.Run("lookup is bound to the requesting user and does not consume", func(t *testing.T) {
		id, err := s.StartConsent(ctx, consentLogin(), "st")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.LookupConsent(ctx, id, "usr_2"); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("foreign lookup = %v", err)
		}
		if _, _, err := s.DecideConsent(ctx, id, "usr_2", true); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("foreign decision = %v", err)
		}
		for range 2 {
			req, err := s.LookupConsent(ctx, id, "usr_1")
			if err != nil || req.OrgID != "org_1" || req.ClientID != AgentClientID || req.RedirectURI != "http://127.0.0.1:9000/cb" {
				t.Fatalf("lookup = %+v, %v", req, err)
			}
		}
	})

	t.Run("approve issues a single-use code bound to the original pkce", func(t *testing.T) {
		id, _ := s.StartConsent(ctx, consentLogin(), "st&x")
		redirect, req, err := s.DecideConsent(ctx, id, "usr_1", true)
		if err != nil || req.OrgID != "org_1" {
			t.Fatalf("approve = %v, %v", req, err)
		}
		u, err := url.Parse(redirect)
		if err != nil || u.Host != "127.0.0.1:9000" || u.Query().Get("state") != "st&x" || u.Query().Get("error") != "" {
			t.Fatalf("approve redirect = %q", redirect)
		}
		got, err := s.RedeemAuthCode(ctx, u.Query().Get("code"), testVerifier, "http://127.0.0.1:9000/cb")
		if err != nil || got.UserID != "usr_1" || got.OrgID != "org_1" {
			t.Fatalf("redeem = %+v, %v", got, err)
		}
		if _, _, err := s.DecideConsent(ctx, id, "usr_1", true); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("second decision = %v", err)
		}
		if _, err := s.LookupConsent(ctx, id, "usr_1"); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("lookup after decision = %v", err)
		}
	})

	t.Run("deny returns access_denied without a code", func(t *testing.T) {
		id, _ := s.StartConsent(ctx, consentLogin(), "st")
		redirect, _, err := s.DecideConsent(ctx, id, "usr_1", false)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(redirect)
		if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" || u.Query().Get("state") != "st" {
			t.Fatalf("deny redirect = %q", redirect)
		}
	})

	t.Run("expired consent cannot be decided", func(t *testing.T) {
		id, _ := s.StartConsent(ctx, consentLogin(), "st")
		now = now.Add(ConsentTTL)
		if _, _, err := s.DecideConsent(ctx, id, "usr_1", true); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("expired decision = %v", err)
		}
	})

	t.Run("malformed ids are rejected", func(t *testing.T) {
		for _, id := range []string{"", strings.Repeat("a", maxCodeBytes+1), "unknown"} {
			if _, err := s.LookupConsent(ctx, id, "usr_1"); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("lookup %q = %v", id, err)
			}
		}
	})
}

func TestDeviceLookupAndDeny(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newClockedService(t, func() time.Time { return now })
	ctx := context.Background()
	start, err := s.StartDevice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		req, err := s.LookupDevice(ctx, strings.ToLower(start.UserCode))
		if err != nil || req.UserCode != start.UserCode || !req.ExpiresAt.Equal(now.Add(DeviceCodeTTL)) {
			t.Fatalf("lookup = %+v, %v", req, err)
		}
	}
	if _, err := s.LookupDevice(ctx, "nope"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("malformed lookup = %v", err)
	}
	if err := s.DenyDevice(ctx, start.UserCode); err != nil {
		t.Fatal(err)
	}
	if err := s.DenyDevice(ctx, start.UserCode); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("second deny = %v", err)
	}
	if err := s.ApproveDevice(ctx, start.UserCode, AgentLogin{OrgID: "org_1", UserID: "usr_1"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("approve after deny = %v", err)
	}
	if _, err := s.LookupDevice(ctx, start.UserCode); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("lookup after deny = %v", err)
	}
	if _, err := s.PollDevice(ctx, start.DeviceCode); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("poll after deny = %v", err)
	}
	now = now.Add(DevicePollInterval)
	if _, err := s.PollDevice(ctx, start.DeviceCode); !errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("denial must be reported once = %v", err)
	}

	expiring, _ := s.StartDevice(ctx)
	now = now.Add(DeviceCodeTTL)
	if _, err := s.LookupDevice(ctx, expiring.UserCode); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired lookup = %v", err)
	}
}
