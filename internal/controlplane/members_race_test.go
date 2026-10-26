package controlplane

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestConcurrentOwnerDisablesKeepAnActiveOwner(t *testing.T) {
	srv, api, _ := newFaultAPI(t, "org")
	ownerA, meA := register(t, srv, "a@example.com", http.StatusCreated)
	orgID := meA.ActiveOrgID
	inv := invite(t, ownerA, srv, "b@example.com", "owner")
	ownerB, meB := registerTok(t, srv, "b@example.com", inv.InviteToken, http.StatusCreated)
	doc(t, ownerB, srv, http.MethodPost, "/api/v1/auth/switch-org", `{"org_id":"`+orgID+`"}`, http.StatusOK, nil)

	var wg sync.WaitGroup
	disable := func(c *http.Client, targetID string) {
		defer wg.Done()
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/members/"+targetID, strings.NewReader(`{"disabled":true}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
	wg.Add(2)
	go disable(ownerA, meB.ID)
	go disable(ownerB, meA.ID)
	wg.Wait()

	ms, err := api.data.ListMembershipsByOrg(context.Background(), orgID)
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, m := range ms {
		if m.Role == store.RoleOwner && m.DisabledAt == nil {
			owners++
		}
	}
	if owners == 0 {
		t.Fatal("concurrent disables left the organization without an owner")
	}
}
