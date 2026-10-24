package storetest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func RunAccessLog(t *testing.T, open func(t *testing.T) store.DataStore) {
	ds := open(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	event := func(org string, offset time.Duration, decision, endpoint string) *store.AccessEvent {
		return &store.AccessEvent{
			ID: store.NewID("acc"), OrgID: org, At: base.Add(offset), Source: store.AccessSourceIngress, Decision: decision,
			Reason: "policy", PrincipalType: "User", PrincipalID: "usr_1", SessionID: "ses_1", AgentID: "ag_1", ExitAgentID: "ag_exit", EndpointID: endpoint,
			PolicyIDs: []string{"p0", "p1"}, ClientIP: "192.0.2.1", Method: "GET", Path: "/x", Status: 200, Protocol: "http",
			BytesIn: 10, BytesOut: 20, DurationMs: 30, LatencyMs: 4,
		}
	}
	first := event("org_a", -3*time.Hour, store.AccessAllow, "ep_1")
	events := []*store.AccessEvent{
		first,
		event("org_a", -2*time.Hour, store.AccessDeny, "ep_1"),
		event("org_a", -time.Hour, store.AccessAllow, "ep_2"),
		event("org_b", -time.Hour, store.AccessAllow, "ep_9"),
	}
	if err := ds.AppendAccessEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	if err := ds.AppendAccessEvents(ctx, nil); err != nil {
		t.Fatal(err)
	}

	all, err := ds.ListAccessEvents(ctx, "org_a", store.AccessLogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].EndpointID != "ep_2" {
		t.Fatalf("org_a events = %d, newest first: %+v", len(all), all)
	}
	got := all[2]
	if !got.At.Equal(first.At) || !slices.Equal(got.PolicyIDs, first.PolicyIDs) || got.Status != 200 || got.BytesOut != 20 || got.LatencyMs != 4 || got.ClientIP != first.ClientIP || got.ExitAgentID != first.ExitAgentID {
		t.Fatalf("round trip = %+v, want %+v", got, first)
	}

	tests := []struct {
		name string
		q    store.AccessLogQuery
		want int
	}{
		{"decision", store.AccessLogQuery{Decision: store.AccessDeny}, 1},
		{"endpoint", store.AccessLogQuery{EndpointID: "ep_1"}, 2},
		{"principal", store.AccessLogQuery{PrincipalID: "usr_1", Source: store.AccessSourceIngress}, 3},
		{"other source", store.AccessLogQuery{Source: store.AccessSourceEgress}, 0},
		{"since", store.AccessLogQuery{Since: base.Add(-90 * time.Minute)}, 1},
		{"until", store.AccessLogQuery{Until: base.Add(-90 * time.Minute)}, 2},
		{"limit", store.AccessLogQuery{Limit: 1}, 1},
		{"foreign endpoint", store.AccessLogQuery{EndpointID: "ep_9"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ds.ListAccessEvents(ctx, "org_a", tc.q)
			if err != nil || len(out) != tc.want {
				t.Fatalf("got %d (%v), want %d", len(out), err, tc.want)
			}
		})
	}

	n, err := ds.PruneAccessEvents(ctx, base.Add(-90*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("pruned %d (%v), want 2", n, err)
	}
	if left, _ := ds.ListAccessEvents(ctx, "org_a", store.AccessLogQuery{}); len(left) != 1 {
		t.Fatalf("after prune = %d", len(left))
	}
	if left, _ := ds.ListAccessEvents(ctx, "org_b", store.AccessLogQuery{}); len(left) != 1 {
		t.Fatalf("org_b after prune = %d", len(left))
	}
}
