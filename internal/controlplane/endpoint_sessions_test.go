package controlplane

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/token"
)

func (f *browserFixture) endpointSession(t *testing.T, org, endpoint, userID string) identity.Issued {
	t.Helper()
	issued, err := f.id.Issue(context.Background(), identity.IssueRequest{
		OrgID: org, Subject: authz.EntityRef{Type: authz.EntityUser, ID: userID}, Type: token.TypeSession,
		AuthMethod: authz.AuthMethodPassword, TTL: time.Hour, EndpointID: endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func TestEndpointSessionsListAndRevoke(t *testing.T) {
	f := newBrowserFixture(t)
	owner, ownerMe := register(t, f.srv, "owner@example.com", http.StatusCreated)
	f.endpoint(t, "ep_app", defaultOrgID, "app", nil)
	f.endpoint(t, "ep_other", "org_other", "other", nil)
	first := f.endpointSession(t, defaultOrgID, "ep_app", ownerMe.ID)
	second := f.endpointSession(t, defaultOrgID, "ep_app", ownerMe.ID)
	foreign := f.endpointSession(t, "org_other", "ep_other", ownerMe.ID)
	ctx := context.Background()

	var listed []endpointSessionDTO
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/endpoints/ep_app/sessions", "", http.StatusOK, &listed)
	if len(listed) != 2 {
		t.Fatalf("listed %+v", listed)
	}
	listedIDs := []string{listed[0].ID, listed[1].ID}
	slices.Sort(listedIDs)
	wantIDs := []string{first.Session.ID, second.Session.ID}
	slices.Sort(wantIDs)
	if !slices.Equal(listedIDs, wantIDs) {
		t.Fatalf("listed %v, want %v", listedIDs, wantIDs)
	}
	for _, session := range listed {
		if session.Email != "owner@example.com" || session.SubjectType != "user" {
			t.Fatalf("listed session %+v", session)
		}
	}
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/endpoints/ep_other/sessions", "", http.StatusNotFound, nil)
	doc(t, owner, f.srv, http.MethodDelete, "/api/v1/endpoints/ep_app/sessions/"+foreign.Session.ID, "", http.StatusNotFound, nil)
	if _, err := f.id.Authenticate(ctx, foreign.Token, token.TypeSession); err != nil {
		t.Fatalf("another org's session was revoked: %v", err)
	}

	doc(t, owner, f.srv, http.MethodDelete, "/api/v1/endpoints/ep_app/sessions/"+first.Session.ID, "", http.StatusNoContent, nil)
	if _, err := f.id.Authenticate(ctx, first.Token, token.TypeSession); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("revoked session still authenticates: %v", err)
	}
	if _, err := f.id.Authenticate(ctx, second.Token, token.TypeSession); err != nil {
		t.Fatalf("sibling session revoked: %v", err)
	}

	third := f.endpointSession(t, defaultOrgID, "ep_app", ownerMe.ID)
	var revoked revokedSessionsDTO
	doc(t, owner, f.srv, http.MethodDelete, "/api/v1/endpoints/ep_app/sessions", "", http.StatusOK, &revoked)
	if revoked.Revoked != 2 {
		t.Fatalf("revoked %d, want 2", revoked.Revoked)
	}
	for _, issued := range []identity.Issued{second, third} {
		if _, err := f.id.Authenticate(ctx, issued.Token, token.TypeSession); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatalf("session survived revoke all: %v", err)
		}
	}
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/endpoints/ep_app/sessions", "", http.StatusOK, &listed)
	if len(listed) != 0 {
		t.Fatalf("listed %+v after revoke all", listed)
	}
}

func TestEndpointSessionsRequireEndpointPermissions(t *testing.T) {
	f := newBrowserFixture(t)
	owner, _ := register(t, f.srv, "owner@example.com", http.StatusCreated)
	f.endpoint(t, "ep_app", defaultOrgID, "app", nil)
	anonymous := newJarClient()
	doc(t, anonymous, f.srv, http.MethodGet, "/api/v1/endpoints/ep_app/sessions", "", http.StatusUnauthorized, nil)
	doc(t, anonymous, f.srv, http.MethodDelete, "/api/v1/endpoints/ep_app/sessions", "", http.StatusUnauthorized, nil)
	doc(t, owner, f.srv, http.MethodDelete, "/api/v1/endpoints/ep_missing/sessions", "", http.StatusNotFound, nil)
}
