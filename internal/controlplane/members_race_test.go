package controlplane

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestConcurrentOwnerDemotionsKeepAnOwner(t *testing.T) {
	srv, api, data := newFaultAPI(t, "org")
	ownerA, meA := register(t, srv, "a@example.com", http.StatusCreated)
	orgID := meA.ActiveOrgID
	inv := invite(t, ownerA, srv, "b@example.com", "owner")
	ownerB, meB := registerTok(t, srv, "b@example.com", inv.InviteToken, http.StatusCreated)
	doc(t, ownerB, srv, http.MethodPost, "/api/v1/auth/switch-org", `{"org_id":"`+orgID+`"}`, http.StatusOK, nil)

	var arrived atomic.Int32
	release := make(chan struct{})
	hook := func() {
		if arrived.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(400 * time.Millisecond):
		}
	}
	data.onListMembers.Store(&hook)

	var wg sync.WaitGroup
	demote := func(c *http.Client, targetID string) {
		defer wg.Done()
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/members/"+targetID, strings.NewReader(`{"role":"member"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
	wg.Add(2)
	go demote(ownerA, meB.ID)
	go demote(ownerB, meA.ID)
	wg.Wait()

	ms, err := api.data.ListMembershipsByOrg(context.Background(), orgID)
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, m := range ms {
		if m.Role == store.RoleOwner {
			owners++
		}
	}
	if owners == 0 {
		t.Fatal("concurrent demotions left the organization without an owner")
	}
}
