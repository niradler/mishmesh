package controlplane

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentInviteRedemptionCreatesOneUser(t *testing.T) {
	srv, api, _ := newFaultAPI(t, "invite")
	owner, me := register(t, srv, "owner@example.com", http.StatusCreated)
	inv := invite(t, owner, srv, "new@example.com", "member")

	const racers = 6
	statuses := make([]int, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body := `{"email":"new@example.com","password":"supersecret","name":"N","invite_token":"` + inv.InviteToken + `"}`
			resp, err := newJarClient().Post(srv.URL+"/api/v1/auth/register", "application/json", strings.NewReader(body))
			if err != nil {
				return
			}
			resp.Body.Close()
			statuses[i] = resp.StatusCode
		}()
	}
	close(start)
	wg.Wait()

	created := 0
	for _, s := range statuses {
		if s == http.StatusCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("statuses = %v, want exactly one 201", statuses)
	}
	ms, err := api.data.ListMembershipsByOrg(context.Background(), me.ActiveOrgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("memberships = %d, want owner + one invitee", len(ms))
	}
	if liveInvites(t, api, "new@example.com") != 0 {
		t.Fatal("invite still redeemable after signup")
	}
}
