package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/token"
)

func (s *ztStack) refreshGrant(refresh string) (int, map[string]any) {
	s.t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {identity.AgentClientID}, "refresh_token": {refresh}}
	resp, err := http.Post(s.api.URL+"/oauth2/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestZeroTrustRefreshGraceKeepsReuseDetection(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@example.com", "")
	tokens, err := s.identity.IssueAgentUser(s.ctx, identity.AgentLogin{OrgID: owner.org, UserID: owner.id, AuthMethod: authz.AuthMethodPassword})
	if err != nil {
		t.Fatal(err)
	}

	status, rotated := s.refreshGrant(tokens.RefreshToken)
	if status != http.StatusOK {
		t.Fatalf("first refresh: %d %v", status, rotated)
	}
	status, retried := s.refreshGrant(tokens.RefreshToken)
	if status != http.StatusOK {
		t.Fatalf("retried refresh inside the grace window: %d %v", status, retried)
	}
	for _, body := range []map[string]any{rotated, retried} {
		if _, err := s.identity.Authenticate(s.ctx, body["access_token"].(string), token.TypeAgentUser); err != nil {
			t.Fatalf("access token after grace retry rejected: %v", err)
		}
	}

	status, replay := s.refreshGrant(tokens.RefreshToken)
	if status != http.StatusBadRequest || replay["error"] != "invalid_grant" {
		t.Fatalf("second replay: %d %v, want 400 invalid_grant", status, replay)
	}
	if _, err := s.identity.Authenticate(s.ctx, rotated["access_token"].(string), token.TypeAgentUser); err == nil {
		t.Fatal("session survived a refresh token replayed twice")
	}
	if status, _ := s.refreshGrant(rotated["refresh_token"].(string)); status != http.StatusBadRequest {
		t.Fatalf("latest refresh after reuse detection = %d, want 400", status)
	}
}
