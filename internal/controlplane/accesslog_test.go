package controlplane

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestAccessLogFiltersAndValidates(t *testing.T) {
	f := newTenantFixture(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	events := []*store.AccessEvent{
		{ID: "acc_1", OrgID: f.orgA, At: base, Source: store.AccessSourceIngress, Decision: store.AccessAllow, PrincipalID: f.userA, EndpointID: "ep_x", PolicyIDs: []string{"p1", "p2"}},
		{ID: "acc_2", OrgID: f.orgA, At: base.Add(time.Minute), Source: store.AccessSourceIngress, Decision: store.AccessDeny, Reason: "policy_deny", PrincipalID: f.userA},
		{ID: "acc_3", OrgID: f.orgA, At: base.Add(2 * time.Minute), Source: store.AccessSourceEgress, Decision: store.AccessDeny, Destination: "198.51.100.1:443"},
		{ID: "acc_4", OrgID: f.orgB, At: base, Source: store.AccessSourceIngress, Decision: store.AccessAllow},
	}
	if err := f.api.data.AppendAccessEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	ids := func(query string) []string {
		t.Helper()
		var out []accessEventDTO
		doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/access-log"+query, "", http.StatusOK, &out)
		got := make([]string, 0, len(out))
		for _, e := range out {
			got = append(got, e.ID)
		}
		return got
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"acc_3", "acc_2", "acc_1"}},
		{"?decision=deny", []string{"acc_3", "acc_2"}},
		{"?source=egress", []string{"acc_3"}},
		{"?principal=" + f.userA + "&decision=allow", []string{"acc_1"}},
		{"?endpoint=ep_x", []string{"acc_1"}},
		{"?since=" + url.QueryEscape(base.Add(time.Minute).Format(time.RFC3339)), []string{"acc_3", "acc_2"}},
		{"?until=" + url.QueryEscape(base.Add(time.Minute).Format(time.RFC3339)), []string{"acc_1"}},
		{"?limit=1", []string{"acc_3"}},
	}
	for _, tt := range tests {
		got := ids(tt.query)
		if len(got) != len(tt.want) {
			t.Errorf("%q = %v want %v", tt.query, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%q = %v want %v", tt.query, got, tt.want)
				break
			}
		}
	}
	var first []accessEventDTO
	doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/access-log?endpoint=ep_x", "", http.StatusOK, &first)
	if len(first) != 1 || len(first[0].PolicyIDs) != 2 || first[0].PolicyIDs[1] != "p2" {
		t.Fatalf("policy ids = %+v", first)
	}
	for _, bad := range []string{"?decision=maybe", "?source=ftp", "?since=yesterday", "?limit=0", "?limit=1001", "?limit=x"} {
		doc(t, f.alice, f.srv, http.MethodGet, "/api/v1/access-log"+bad, "", http.StatusBadRequest, nil)
	}
}
