package proxy

import (
	"context"
	"fmt"
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
				got := c.orderTargets("ep_1", "", &tc.policy)
				if !slices.Equal(got, want) {
					t.Fatalf("call %d: got %v want %v", i, got, want)
				}
			}
		})
	}
}

func stickyPolicy(targets ...string) *store.EndpointPolicy {
	return &store.EndpointPolicy{LoadBalance: store.LoadBalanceSticky, ProxyTargets: targets}
}

func TestStickySameIPSameTarget(t *testing.T) {
	c := newConn(nil, nil, nil)
	pol := stickyPolicy("a:1", "b:1", "c:1", "d:1")
	for _, ip := range []string{"192.0.2.1", "198.51.100.7", "2001:db8::1"} {
		first := c.orderTargets("ep_1", ip, pol)
		for i := 0; i < 200; i++ {
			if got := c.orderTargets("ep_1", ip, pol); !slices.Equal(got, first) {
				t.Fatalf("ip %s call %d: got %v want %v", ip, i, got, first)
			}
		}
	}
}

func TestStickySpreadsAcrossTargets(t *testing.T) {
	c := newConn(nil, nil, nil)
	pol := stickyPolicy("a:1", "b:1", "c:1")
	counts := map[string]int{}
	for i := 0; i < 600; i++ {
		counts[c.orderTargets("ep_1", fmt.Sprintf("10.0.%d.%d", i/250, i%250), pol)[0]]++
	}
	for _, target := range pol.ProxyTargets {
		if counts[target] < 100 {
			t.Fatalf("target %s got %d of 600 clients: %v", target, counts[target], counts)
		}
	}
}

func TestStickyUnhealthyTargetMovesOnlyItsClients(t *testing.T) {
	targets := []string{"a:1", "b:1", "c:1", "d:1"}
	pol := stickyPolicy(targets...)
	c := newConn(nil, nil, nil)
	before := map[string][]string{}
	for i := 0; i < 400; i++ {
		ip := fmt.Sprintf("172.16.%d.%d", i/250, i%250)
		before[ip] = c.orderTargets("ep_1", ip, pol)
	}
	down := "b:1"
	c.SetTargetHealth(fakeHealth{down: false})
	moved := 0
	for ip, was := range before {
		now := c.orderTargets("ep_1", ip, pol)
		if now[len(now)-1] != down {
			t.Fatalf("ip %s: unhealthy target not last: %v", ip, now)
		}
		if was[0] == down {
			moved++
			if now[0] != was[1] {
				t.Fatalf("ip %s: moved to %s, want next-ranked %s", ip, now[0], was[1])
			}
			continue
		}
		if now[0] != was[0] {
			t.Fatalf("ip %s pinned to %s moved to %s", ip, was[0], now[0])
		}
	}
	if moved == 0 {
		t.Fatal("no client was pinned to the downed target")
	}
}

func TestStickyWithoutClientIPRoundRobins(t *testing.T) {
	c := newConn(nil, nil, nil)
	pol := stickyPolicy("a:1", "b:1", "c:1")
	want := [][]string{{"a:1", "b:1", "c:1"}, {"b:1", "c:1", "a:1"}, {"c:1", "a:1", "b:1"}}
	for i, w := range want {
		if got := c.orderTargets("ep_1", "", pol); !slices.Equal(got, w) {
			t.Fatalf("call %d: got %v want %v", i, got, w)
		}
	}
}

func TestStickyAllUnhealthyKeepsConfiguredOrder(t *testing.T) {
	c := newConn(nil, nil, nil)
	c.SetTargetHealth(fakeHealth{"a:1": false, "b:1": false})
	got := c.orderTargets("ep_1", "192.0.2.1", stickyPolicy("a:1", "b:1"))
	if !slices.Equal(got, []string{"a:1", "b:1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestOpenStreamFailsOverToLiveTarget(t *testing.T) {
	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()
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
