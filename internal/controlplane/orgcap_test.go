package controlplane

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

func newOrgCapAPI(t *testing.T, maxOrgs int) *httptest.Server {
	t.Helper()
	data, err := sqlite.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	api := New(data, memory.NewConnStore(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetMaxOrgsPerUser(maxOrgs)
	api.ConfigureAuth(AuthOptions{Enabled: true, PasswordEnabled: true, SignupMode: "org"})
	mux := http.NewServeMux()
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOrgCreationCapPerUser(t *testing.T) {
	tests := []struct {
		name    string
		maxOrgs int
		extra   int
		wantOK  int
	}{
		{"default cap of 3 allows two extra", 3, 3, 2},
		{"cap of 1 allows none", 1, 2, 0},
		{"unlimited", 0, 5, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newOrgCapAPI(t, tc.maxOrgs)
			alice, _ := register(t, srv, "alice@example.com", http.StatusCreated)
			bob, _ := register(t, srv, "bob@example.com", http.StatusCreated)
			created := 0
			for i := 0; i < tc.extra; i++ {
				want := http.StatusCreated
				if i >= tc.wantOK {
					want = http.StatusConflict
				}
				doc(t, alice, srv, http.MethodPost, "/api/v1/orgs", `{"name":"org"}`, want, nil)
				if want == http.StatusCreated {
					created++
				}
			}
			if created != tc.wantOK {
				t.Fatalf("created %d want %d", created, tc.wantOK)
			}
			doc(t, bob, srv, http.MethodPost, "/api/v1/orgs", `{"name":"bobs"}`, expectForCap(tc.maxOrgs), nil)
		})
	}
}

func expectForCap(maxOrgs int) int {
	if maxOrgs == 1 {
		return http.StatusConflict
	}
	return http.StatusCreated
}

func TestOrgCapIgnoresNonOwnerMemberships(t *testing.T) {
	srv := newOrgCapAPI(t, 2)
	owner, ownerMe := register(t, srv, "owner@example.com", http.StatusCreated)
	inv := invite(t, owner, srv, "guest@example.com", "member")
	guest, _ := registerTok(t, srv, "guest@example.com", inv.InviteToken, http.StatusCreated)
	doc(t, guest, srv, http.MethodPost, "/api/v1/auth/switch-org", `{"org_id":"`+ownerMe.ActiveOrgID+`"}`, http.StatusOK, nil)
	doc(t, guest, srv, http.MethodPost, "/api/v1/orgs", `{"name":"mine"}`, http.StatusCreated, nil)
}
