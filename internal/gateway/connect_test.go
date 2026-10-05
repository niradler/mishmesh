package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

type faultyStore struct {
	store.DataStore
	tokenErr error
	agentErr error
}

func (f *faultyStore) GetTokenByHash(ctx context.Context, hash string) (*store.Token, error) {
	if f.tokenErr != nil {
		return nil, f.tokenErr
	}
	return f.DataStore.GetTokenByHash(ctx, hash)
}

func (f *faultyStore) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	if f.agentErr != nil {
		return nil, f.agentErr
	}
	return f.DataStore.GetAgent(ctx, id)
}

func TestAgentConnectDistinguishesStoreFailureFromAuthFailure(t *testing.T) {
	dbDown := errors.New("connection refused")
	tests := []struct {
		name      string
		auth      string
		tokenErr  error
		agentErr  error
		wantCode  int
		wantRetry bool
	}{
		{"missing bearer", "", nil, nil, http.StatusUnauthorized, false},
		{"unknown token", "Bearer nope", nil, nil, http.StatusUnauthorized, false},
		{"token lookup db error", "Bearer anything", dbDown, nil, http.StatusServiceUnavailable, true},
		{"token lookup wrapped not found", "Bearer anything", store.ErrNotFound, nil, http.StatusUnauthorized, false},
		{"agent lookup db error", "Bearer valid", nil, dbDown, http.StatusServiceUnavailable, true},
		{"agent lookup not found", "Bearer valid", nil, store.ErrNotFound, http.StatusForbidden, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, ag, _, data := newRegisterFixture(t, Options{})
			raw, hash, err := store.GenerateToken()
			if err != nil {
				t.Fatal(err)
			}
			if err := data.CreateToken(context.Background(), &store.Token{ID: "tok1", OrgID: ag.OrgID, AgentID: ag.ID, Hash: hash}); err != nil {
				t.Fatal(err)
			}
			g.data = &faultyStore{DataStore: data, tokenErr: tt.tokenErr, agentErr: tt.agentErr}
			auth := tt.auth
			if auth == "Bearer valid" {
				auth = "Bearer " + raw
			}
			req := httptest.NewRequest(http.MethodGet, "/agent/connect", nil)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			rec := httptest.NewRecorder()
			g.HandleAgentConnect(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After") != ""; got != tt.wantRetry {
				t.Fatalf("Retry-After present = %v, want %v", got, tt.wantRetry)
			}
		})
	}
}
