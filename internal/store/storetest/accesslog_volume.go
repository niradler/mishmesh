package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func accessEvents(count int, at time.Time, org string) []*store.AccessEvent {
	events := make([]*store.AccessEvent, count)
	for i := range events {
		events[i] = &store.AccessEvent{
			ID: store.NewID("acc"), OrgID: org, At: at.Add(time.Duration(i) * time.Microsecond), Source: store.AccessSourceIngress, Decision: store.AccessAllow,
			Reason: "policy", PrincipalType: "User", PrincipalID: "usr_1", SessionID: "ses_1", AgentID: "ag_1", ExitAgentID: "ag_exit", EndpointID: "ep_1",
			PolicyIDs: []string{"p0", "p1"}, ClientIP: "192.0.2.1", Method: "GET", Path: "/x", Status: 200, Protocol: "http",
			BytesIn: 10, BytesOut: 20, DurationMs: 30, LatencyMs: 4,
		}
	}
	return events
}

func RunAccessLogLargeBatch(t *testing.T, open func(t *testing.T) store.DataStore) {
	ds := open(t)
	ctx := context.Background()
	const total = 5000
	if err := ds.AppendAccessEvents(ctx, accessEvents(total, time.Now().UTC(), "org_big")); err != nil {
		t.Fatal(err)
	}
	got, err := ds.ListAccessEvents(ctx, "org_big", store.AccessLogQuery{Limit: store.MaxAccessLogLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != store.MaxAccessLogLimit {
		t.Fatalf("listed %d events, want %d", len(got), store.MaxAccessLogLimit)
	}
	pruned, err := ds.PruneAccessEvents(ctx, time.Now().Add(time.Hour))
	if err != nil || pruned != total {
		t.Fatalf("pruned %d, %v; want %d", pruned, err, total)
	}
}

func RunAccessLogPruneMakesProgressUnderDeadline(t *testing.T, open func(t *testing.T) store.DataStore) {
	ds := open(t)
	t.Cleanup(func() { _, _ = ds.PruneAccessEvents(context.Background(), time.Now().Add(time.Hour)) })
	const total = 60000
	old := time.Now().Add(-48 * time.Hour).UTC()
	for offset := 0; offset < total; offset += 5000 {
		if err := ds.AppendAccessEvents(context.Background(), accessEvents(5000, old.Add(time.Duration(offset)*time.Microsecond), "org_old")); err != nil {
			t.Fatal(err)
		}
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	const maxAttempts = 400
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*raceDetectorSlowdown*time.Millisecond)
		_, err := ds.PruneAccessEvents(ctx, cutoff)
		cancel()
		if err == nil {
			t.Logf("backlog of %d rows drained after %d deadline-limited prune attempts", total, attempt)
			return
		}
	}
	t.Fatalf("backlog of %d rows not drained after %d prune attempts cut off by their deadline, so a backlog larger than one deadline is never reclaimed", total, maxAttempts)
}

func BenchAppendAccessEvents(b *testing.B, newStore func(testing.TB) store.DataStore) {
	for _, size := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("batch%d", size), func(b *testing.B) {
			ds := newStore(b)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				events := accessEvents(size, time.Now().UTC(), "org_bench")
				b.StartTimer()
				if err := ds.AppendAccessEvents(ctx, events); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(size)*float64(b.N)/b.Elapsed().Seconds(), "events/s")
		})
	}
}
