package controlplane

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

type userLookupCounter struct {
	store.DataStore
	single atomic.Int32
	batch  atomic.Int32
}

func (c *userLookupCounter) GetUserByID(ctx context.Context, id string) (*store.User, error) {
	c.single.Add(1)
	return c.DataStore.GetUserByID(ctx, id)
}

func (c *userLookupCounter) GetUsersByIDs(ctx context.Context, ids []string) ([]*store.User, error) {
	c.batch.Add(1)
	return c.DataStore.GetUsersByIDs(ctx, ids)
}

func (c *userLookupCounter) lookups() int32 { return c.single.Load() + c.batch.Load() }

func addUsers(t *testing.T, data store.DataStore, orgID string, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, n)
	for i := range ids {
		id := store.NewID("usr")
		if err := data.CreateUser(ctx, &store.User{ID: id, Email: id + "@example.com", Name: "n", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := data.CreateMembership(ctx, &store.Membership{OrgID: orgID, UserID: id, Role: store.RoleMember, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	return ids
}

func TestListMembersLooksUpUsersInOneQuery(t *testing.T) {
	srv, api, _ := newFaultAPI(t, "org")
	owner, me := register(t, srv, "owner@example.com", http.StatusCreated)
	addUsers(t, api.data, me.ActiveOrgID, 40)
	counter := &userLookupCounter{DataStore: api.data}
	api.data = counter

	var members []memberDTO
	doc(t, owner, srv, http.MethodGet, "/api/v1/members", "", http.StatusOK, &members)
	if len(members) != 41 {
		t.Fatalf("members = %d, want 41", len(members))
	}
	t.Logf("user lookups for 41 members: GetUserByID=%d GetUsersByIDs=%d", counter.single.Load(), counter.batch.Load())
	if got := counter.lookups(); got > 2 || counter.batch.Load() != 1 {
		t.Fatalf("user lookups = %d, want one batch plus the session user", got)
	}
}

func TestListEndpointSessionsLooksUpUsersInOneQuery(t *testing.T) {
	f := newBrowserFixture(t)
	owner, ownerMe := register(t, f.srv, "owner@example.com", http.StatusCreated)
	f.endpoint(t, "ep_app", defaultOrgID, "app", nil)
	f.endpointSession(t, defaultOrgID, "ep_app", ownerMe.ID)
	for _, id := range addUsers(t, f.data, defaultOrgID, 30) {
		f.endpointSession(t, defaultOrgID, "ep_app", id)
	}
	counter := &userLookupCounter{DataStore: f.api.data}
	f.api.data = counter

	var listed []endpointSessionDTO
	doc(t, owner, f.srv, http.MethodGet, "/api/v1/endpoints/ep_app/sessions", "", http.StatusOK, &listed)
	if len(listed) != 31 {
		t.Fatalf("sessions = %d, want 31", len(listed))
	}
	for _, s := range listed {
		if s.Email == "" {
			t.Fatalf("session %s has no email", s.ID)
		}
	}
	t.Logf("user lookups for 31 sessions of 31 users: GetUserByID=%d GetUsersByIDs=%d", counter.single.Load(), counter.batch.Load())
	if got := counter.lookups(); got > 2 || counter.batch.Load() != 1 {
		t.Fatalf("user lookups = %d, want one batch plus the session user", got)
	}
}
