package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/token"
)

func TestSafeReturnPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/app/x?y=1", true},
		{"", false},
		{"app", false},
		{"//evil.example", false},
		{`/\evil.example`, false},
		{`/a\b`, false},
		{"/a\nb", false},
		{"https://evil.example/", false},
	}
	for _, tt := range tests {
		if got := SafeReturnPath(tt.path); got != tt.want {
			t.Errorf("SafeReturnPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestEndpointLoginIsSingleUse(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	in := EndpointLogin{EndpointID: "ep_1", OrgID: "org_1", UserID: "usr_1", AuthMethod: authz.AuthMethodPassword, AuthTime: time.Now().UTC().Truncate(time.Second), Return: "/x"}
	code, err := s.PutEndpointLogin(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.TakeEndpointLogin(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if got.EndpointID != in.EndpointID || got.UserID != in.UserID || got.Return != in.Return || !got.AuthTime.Equal(in.AuthTime) {
		t.Fatalf("login = %+v", got)
	}
	if _, err := s.TakeEndpointLogin(ctx, code); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("replay accepted: %v", err)
	}
	if _, err := s.TakeEndpointLogin(ctx, "unknown"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown code accepted: %v", err)
	}
	in.Return = "//evil.example"
	if _, err := s.PutEndpointLogin(ctx, in); err == nil {
		t.Fatal("unsafe return accepted")
	}
}

func TestGuestSessionIsEndpointBound(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	issued, err := s.IssueGuest(ctx, "org_1", "ep_1", "a@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	email, err := s.AuthenticateGuest(ctx, issued.Token, "ep_1")
	if err != nil || email != "a@example.com" {
		t.Fatalf("guest = %q, %v", email, err)
	}
	if _, err := s.AuthenticateGuest(ctx, issued.Token, "ep_2"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other endpoint accepted: %v", err)
	}
	if _, err := s.Authenticate(ctx, issued.Token, token.TypeSession); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("guest accepted as principal: %v", err)
	}
	user := issue(t, s, "org_1")
	if _, err := s.AuthenticateGuest(ctx, user.Token, "ep_1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("access token accepted as guest: %v", err)
	}
	if err := s.RevokeSession(ctx, issued.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateGuest(ctx, issued.Token, "ep_1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked guest accepted: %v", err)
	}
}
