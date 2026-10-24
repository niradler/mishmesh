package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/token"
)

const testVerifier = "abcdefghijklmnopqrstuvwxyz0123456789-._~ABCD"

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func newClockedService(t *testing.T, now func() time.Time) *Service {
	t.Helper()
	keys, err := token.NewKeyring(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := New(keys, memory.NewSessionStoreWithClock(now))
	s.SetClock(now)
	return s
}

func TestLoopbackRedirect(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:5000/cb":      true,
		"http://[::1]:5000/cb":          true,
		"http://127.0.0.1/cb":           false,
		"https://127.0.0.1:5000/cb":     false,
		"http://localhost:5000/cb":      false,
		"http://example.com:5000/cb":    false,
		"http://u:p@127.0.0.1:5000/cb":  false,
		"http://127.0.0.1:5000/cb#frag": false,
		"http://10.0.0.1:5000/cb":       false,
		"":                              false,
		"http://127.0.0.1:5000/" + strings.Repeat("a", 600): false,
	}
	for raw, want := range cases {
		if got := LoopbackRedirect(raw); got != want {
			t.Errorf("LoopbackRedirect(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestVerifyPKCE(t *testing.T) {
	c := challengeFor(testVerifier)
	if !ValidCodeChallenge(c) || !VerifyPKCE(c, testVerifier) {
		t.Fatal("valid verifier rejected")
	}
	if VerifyPKCE(c, testVerifier+"x") || VerifyPKCE(c, "short") || VerifyPKCE(c, strings.Repeat("!", 50)) {
		t.Fatal("bad verifier accepted")
	}
	if ValidCodeChallenge("plain") {
		t.Fatal("short challenge accepted")
	}
}

func TestAuthCodeRoundTrip(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	login := AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword, Challenge: challengeFor(testVerifier), RedirectURI: "http://127.0.0.1:9000/cb"}
	if _, err := s.PutAuthCode(ctx, AgentLogin{OrgID: "org_1", UserID: "usr_1", Challenge: "x", RedirectURI: login.RedirectURI}); err == nil {
		t.Fatal("bad challenge accepted")
	}
	code, err := s.PutAuthCode(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemAuthCode(ctx, code, testVerifier, "http://127.0.0.1:9001/cb"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("redirect mismatch = %v", err)
	}
	if _, err := s.RedeemAuthCode(ctx, code, testVerifier, login.RedirectURI); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("code must be single use even after a failed attempt: %v", err)
	}
	code, _ = s.PutAuthCode(ctx, login)
	got, err := s.RedeemAuthCode(ctx, code, testVerifier, login.RedirectURI)
	if err != nil || got.OrgID != "org_1" || got.UserID != "usr_1" {
		t.Fatalf("redeem = %+v, %v", got, err)
	}
	if _, err := s.RedeemAuthCode(ctx, code, testVerifier, login.RedirectURI); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("replay = %v", err)
	}
}

func TestDeviceFlow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newClockedService(t, func() time.Time { return now })
	ctx := context.Background()
	start, err := s.StartDevice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(start.UserCode) != 9 || start.UserCode[4] != '-' {
		t.Fatalf("user code = %q", start.UserCode)
	}
	if _, err := s.PollDevice(ctx, start.DeviceCode); !errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("pending = %v", err)
	}
	if _, err := s.PollDevice(ctx, start.DeviceCode); !errors.Is(err, ErrSlowDown) {
		t.Fatalf("slow down = %v", err)
	}
	if err := s.ApproveDevice(ctx, "BCDF-GHJK", AgentLogin{OrgID: "org_1", UserID: "usr_1"}); !errors.Is(err, ErrUnauthenticated) && start.UserCode != "BCDF-GHJK" {
		t.Fatalf("unknown code = %v", err)
	}
	lower := strings.ToLower(strings.ReplaceAll(start.UserCode, "-", ""))
	if err := s.ApproveDevice(ctx, lower, AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodOIDC}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveDevice(ctx, start.UserCode, AgentLogin{OrgID: "org_2", UserID: "usr_2"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("second approval = %v", err)
	}
	now = now.Add(DevicePollInterval)
	got, err := s.PollDevice(ctx, start.DeviceCode)
	if err != nil || got.OrgID != "org_1" || got.UserID != "usr_1" {
		t.Fatalf("poll = %+v, %v", got, err)
	}
	now = now.Add(DevicePollInterval)
	if _, err := s.PollDevice(ctx, start.DeviceCode); !errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("redeemed device code = %v", err)
	}
	now = now.Add(DeviceCodeTTL)
	if _, err := s.PollDevice(ctx, start.DeviceCode); !errors.Is(err, ErrExpiredCode) {
		t.Fatalf("expired = %v", err)
	}
	if _, err := s.PollDevice(ctx, "garbage"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("garbage = %v", err)
	}
}

func TestAgentUserRefreshRotatesAndRevokes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newClockedService(t, func() time.Time { return now })
	ctx := context.Background()
	first, err := s.IssueAgentUser(ctx, AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	if first.ExpiresIn != AgentAccessTTL {
		t.Fatalf("expires in = %v", first.ExpiresIn)
	}
	p, err := s.Authenticate(ctx, first.AccessToken, token.TypeAgentUser)
	if err != nil || p.Ref != (authz.EntityRef{Type: authz.EntityUser, ID: "usr_1"}) {
		t.Fatalf("access = %+v, %v", p, err)
	}
	if _, err := s.Authenticate(ctx, first.RefreshToken, token.TypeAgentUser); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("refresh token used as access = %v", err)
	}
	if _, err := s.RefreshAgentUser(ctx, first.AccessToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("access token used as refresh = %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := s.Authenticate(ctx, first.AccessToken, token.TypeAgentUser); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired access accepted = %v", err)
	}
	second, err := s.RefreshAgentUser(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.Session.ID != first.Session.ID {
		t.Fatal("refresh must keep the session")
	}
	if _, err := s.Authenticate(ctx, second.AccessToken, token.TypeAgentUser); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, second.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, second.AccessToken, token.TypeAgentUser); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session accepted = %v", err)
	}
	if err := s.RevokeToken(ctx, "garbage"); err != nil {
		t.Fatalf("revoking garbage must succeed silently: %v", err)
	}
}

func TestAgentUserRefreshReuseRevokesSession(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := newClockedService(t, func() time.Time { return now })
	ctx := context.Background()
	var revoked []Revocation
	s.Subscribe(func(r Revocation) { revoked = append(revoked, r) })
	first, err := s.IssueAgentUser(ctx, AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RefreshAgentUser(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(RefreshGrace)
	if _, err := s.RefreshAgentUser(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshReused) || !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("reused refresh = %v, want ErrRefreshReused", err)
	}
	if _, err := s.Authenticate(ctx, second.AccessToken, token.TypeAgentUser); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("access from reused family still valid: %v", err)
	}
	if _, err := s.RefreshAgentUser(ctx, second.RefreshToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("latest refresh after reuse = %v, want unauthenticated", err)
	}
	if len(revoked) != 1 || len(revoked[0].SessionIDs) != 1 || revoked[0].SessionIDs[0] != first.Session.ID {
		t.Fatalf("revocations = %+v", revoked)
	}
}

func TestStaleAccessTokenDoesNotRevokeSession(t *testing.T) {
	s := newClockedService(t, time.Now)
	ctx := context.Background()
	first, err := s.IssueAgentUser(ctx, AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RefreshAgentUser(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, first.AccessToken, token.TypeAgentUser); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("stale access = %v", err)
	}
	if _, err := s.Authenticate(ctx, second.AccessToken, token.TypeAgentUser); err != nil {
		t.Fatalf("stale access token must not revoke the session: %v", err)
	}
}

func TestAgentUserRefreshGraceWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ctx := context.Background()
	login := AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword}

	t.Run("a retried refresh inside the window gets the current tokens once", func(t *testing.T) {
		s := newClockedService(t, func() time.Time { return now })
		first, err := s.IssueAgentUser(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.RefreshAgentUser(ctx, first.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		retried, err := s.RefreshAgentUser(ctx, first.RefreshToken)
		if err != nil {
			t.Fatalf("retry inside the grace window = %v", err)
		}
		if retried.Session.TokenID != second.Session.TokenID {
			t.Fatal("grace retry must return the current generation, not rotate again")
		}
		for _, tok := range []string{second.AccessToken, retried.AccessToken} {
			if _, err := s.Authenticate(ctx, tok, token.TypeAgentUser); err != nil {
				t.Fatalf("access after grace retry = %v", err)
			}
		}
		if _, err := s.RefreshAgentUser(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshReused) {
			t.Fatalf("second replay inside the window = %v, want ErrRefreshReused", err)
		}
		if _, err := s.Authenticate(ctx, second.AccessToken, token.TypeAgentUser); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("session survived a second replay: %v", err)
		}
	})

	t.Run("a token two generations old is reuse even inside the window", func(t *testing.T) {
		s := newClockedService(t, func() time.Time { return now })
		first, err := s.IssueAgentUser(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.RefreshAgentUser(ctx, first.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RefreshAgentUser(ctx, second.RefreshToken); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RefreshAgentUser(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshReused) {
			t.Fatalf("grandparent refresh = %v, want ErrRefreshReused", err)
		}
	})

	t.Run("concurrent refreshes of the same token derive the same generation", func(t *testing.T) {
		s := newClockedService(t, func() time.Time { return now })
		first, err := s.IssueAgentUser(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
		if nextTokenID(first.Session.ID, first.Session.TokenID) != nextTokenID(first.Session.ID, first.Session.TokenID) {
			t.Fatal("next token id must be deterministic")
		}
		if nextTokenID(first.Session.ID, first.Session.TokenID) == nextTokenID("tsn_other", first.Session.TokenID) {
			t.Fatal("next token id must be bound to the session")
		}
		results := make(chan error, 2)
		for range cap(results) {
			go func() {
				_, err := s.RefreshAgentUser(ctx, first.RefreshToken)
				results <- err
			}()
		}
		for range cap(results) {
			if err := <-results; err != nil {
				t.Fatalf("one of two concurrent refreshes failed: %v", err)
			}
		}
		active, err := s.SessionActive(ctx, first.Session.ID)
		if err != nil || !active {
			t.Fatalf("session after two concurrent refreshes active=%v err=%v", active, err)
		}
	})
}

type revokeDuringRotate struct {
	store.SessionStore
	revoke func()
}

func (r revokeDuringRotate) RotateTokenSession(ctx context.Context, ts *store.TokenSession, prev string) error {
	r.revoke()
	return r.SessionStore.RotateTokenSession(ctx, ts, prev)
}

func TestRefreshRacingRevocationDoesNotResurrectSession(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	keys, err := token.NewKeyring(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sessions := memory.NewSessionStoreWithClock(clock)
	ctx := context.Background()
	var sessionID string
	s := New(keys, revokeDuringRotate{SessionStore: sessions, revoke: func() { _ = sessions.DeleteTokenSession(ctx, sessionID) }})
	s.SetClock(clock)
	first, err := s.IssueAgentUser(ctx, AgentLogin{OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword})
	if err != nil {
		t.Fatal(err)
	}
	sessionID = first.Session.ID
	if _, err := s.RefreshAgentUser(ctx, first.RefreshToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("refresh racing a revocation = %v, want unauthenticated", err)
	}
	if _, err := sessions.GetTokenSession(ctx, sessionID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked session came back: %v", err)
	}
}
