package proxy

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type fakeHealth map[string]bool

func (f fakeHealth) TargetHealthy(_, target string) (bool, bool) {
	h, ok := f[target]
	return h, ok
}

func TestOrderTargets(t *testing.T) {
	tests := []struct {
		name   string
		policy store.EndpointPolicy
		health fakeHealth
		want   [][]string
	}{
		{"single", store.EndpointPolicy{ProxyTargets: []string{"a:1"}}, nil,
			[][]string{{"a:1"}, {"a:1"}}},
		{"round robin default", store.EndpointPolicy{ProxyTargets: []string{"a:1", "b:1", "c:1"}}, nil,
			[][]string{{"a:1", "b:1", "c:1"}, {"b:1", "c:1", "a:1"}, {"c:1", "a:1", "b:1"}, {"a:1", "b:1", "c:1"}}},
		{"failover keeps order", store.EndpointPolicy{LoadBalance: store.LoadBalanceFailover, ProxyTargets: []string{"a:1", "b:1"}}, nil,
			[][]string{{"a:1", "b:1"}, {"a:1", "b:1"}}},
		{"unhealthy last", store.EndpointPolicy{LoadBalance: store.LoadBalanceFailover, ProxyTargets: []string{"a:1", "b:1", "c:1"}}, fakeHealth{"a:1": false, "b:1": true},
			[][]string{{"b:1", "c:1", "a:1"}}},
		{"round robin skips unhealthy", store.EndpointPolicy{LoadBalance: store.LoadBalanceRoundRobin, ProxyTargets: []string{"a:1", "b:1", "c:1"}}, fakeHealth{"a:1": false},
			[][]string{{"b:1", "c:1", "a:1"}, {"c:1", "b:1", "a:1"}}},
		{"all unhealthy tried in order", store.EndpointPolicy{LoadBalance: store.LoadBalanceFailover, ProxyTargets: []string{"a:1", "b:1"}}, fakeHealth{"a:1": false, "b:1": false},
			[][]string{{"a:1", "b:1"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newConn(nil, nil, nil)
			if tc.health != nil {
				c.SetTargetHealth(tc.health)
			}
			for i, want := range tc.want {
				got := c.orderTargets("ep_1", &tc.policy)
				if !slices.Equal(got, want) {
					t.Fatalf("call %d: got %v want %v", i, got, want)
				}
			}
		})
	}
}

func TestOpenStreamFailsOverToLiveTarget(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()
	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	go func() {
		c, err := live.Accept()
		if err == nil {
			c.Close()
		}
	}()
	data, err := sqlite.Open(t.TempDir() + "/f.db")
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	ctx := context.Background()
	now := time.Now()
	_ = data.CreateOrg(ctx, &store.Org{ID: "org_default", Name: "d", CreatedAt: now})
	guard, err := NewGuard(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	connector := Register(ctx, data, memory.NewConnStore(), nil, guard)
	ep := &store.Endpoint{
		ID: store.NewID("ep"), AgentID: AgentID, OrgID: "org_default", Kind: store.KindHTTP,
		Method: store.MethodProxy, Lifecycle: store.LifecycleReserved, CreatedAt: now,
		Policy: &store.EndpointPolicy{LoadBalance: store.LoadBalanceFailover, ProxyTargets: []string{deadAddr, live.Addr().String()}},
	}
	if err := data.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	c, err := connector.OpenStream(ctx, ep.ID, store.KindHTTP, nil)
	if err != nil {
		t.Fatalf("failover open: %v", err)
	}
	c.Close()

	ep.Policy.ProxyTargets = []string{deadAddr}
	if err := data.UpdateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if _, err := connector.OpenStream(ctx, ep.ID, store.KindHTTP, nil); err == nil {
		t.Fatal("expected error when every target is dead")
	}
}
