package e2e

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

func (s *ztStack) runUserAgent(owner, user *ztUser, name, gatewayURL string, connects []agent.ConnectSpec) {
	s.t.Helper()
	_, rawToken := owner.createAgent(name)
	tokens, err := s.identity.IssueAgentUser(s.ctx, identity.AgentLogin{OrgID: owner.org, UserID: user.id, AuthMethod: authz.AuthMethodPassword})
	if err != nil {
		s.t.Fatal(err)
	}
	s.runAgent(agent.Options{
		GatewayURL: gatewayURL,
		Token:      rawToken,
		Connects:   connects,
		UserToken:  func(context.Context) (string, error) { return tokens.AccessToken, nil },
	})
}

func ztAwaitTCP(t *testing.T, addr string, within time.Duration) {
	t.Helper()
	ztEventually(t, within, "tcp connect through "+addr, func() bool {
		got, err := tcpRoundTrip(addr, "select 1")
		return err == nil && got == "select 1"
	})
}

func ztRefusedTCP(t *testing.T, addr string) {
	t.Helper()
	for range 3 {
		if got, err := tcpRoundTrip(addr, "select 1"); err == nil {
			t.Fatalf("connect through %s should be refused, got echo %q", addr, got)
		}
	}
}

func TestZeroTrustUserBoundAgentConnect(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@example.com", "")
	dba := owner.invite("dba@example.com")
	dev := owner.invite("dev@example.com")
	ops := owner.invite("ops@example.com")
	group := owner.createGroup("db-users")
	owner.addGroupMember(group, store.GroupMemberUser, dba.id)
	owner.addGroupMember(group, store.GroupMemberUser, ops.id)
	owner.putPolicy(`@id("zt-db-users-connect")
permit (principal in Group::"` + group + `", action == Action::"agent:connect", resource);`)

	originID, originToken := owner.createAgent("db-host")
	s.runAgent(agent.Options{Token: originToken, Endpoints: []agent.EndpointSpec{
		{Name: "postgres", Kind: store.KindTCP, Lifecycle: store.LifecycleEphemeral, Private: true, LocalTarget: tcpEchoServer(t), Policy: connectPolicy("nobody")},
		{Name: "telemetry", Kind: store.KindUDP, Lifecycle: store.LifecycleEphemeral, Private: true, LocalTarget: udpEchoServer(t), Policy: connectPolicy("nobody")},
	}})
	s.waitEndpoint(originID, func(ep *store.Endpoint) bool { return connectNameOf(ep) == "postgres" })
	s.waitEndpoint(originID, func(ep *store.Endpoint) bool { return connectNameOf(ep) == "telemetry" })

	t.Run("group decides connect", func(t *testing.T) {
		cases := []struct {
			name    string
			user    *ztUser
			allowed bool
		}{
			{"member of group", dba, true},
			{"not in group", dev, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				listen := freeAddr(t, "tcp")
				before := s.connect.decisions.Load()
				s.runUserAgent(owner, tc.user, "laptop-"+strings.ReplaceAll(tc.name, " ", "-"), "", []agent.ConnectSpec{{Name: "pg", Endpoint: "postgres", Kind: store.KindTCP, Listen: listen}})
				if tc.allowed {
					ztAwaitTCP(t, listen, 8*time.Second)
					return
				}
				ztEventually(t, 8*time.Second, "denied decision round trip", func() bool {
					_, _ = tcpRoundTrip(listen, "select 1")
					return s.connect.decisions.Load() > before
				})
				ztRefusedTCP(t, listen)
			})
		}
	})

	t.Run("cache hit avoids round trip", func(t *testing.T) {
		listen := freeAddr(t, "tcp")
		s.runUserAgent(owner, dba, "cache-probe", "", []agent.ConnectSpec{{Name: "pg", Endpoint: "postgres", Kind: store.KindTCP, Listen: listen}})
		ztEventually(t, 8*time.Second, "agent ready", func() bool {
			before := s.connect.decisions.Load()
			got, err := tcpRoundTrip(listen, "warm")
			return err == nil && got == "warm" && s.connect.decisions.Load() == before
		})
		cached := s.connect.decisions.Load()
		for i := range 3 {
			if got, err := tcpRoundTrip(listen, "again"); err != nil || got != "again" {
				t.Fatalf("cached connect %d: got %q, err %v", i, got, err)
			}
		}
		if n := s.connect.decisions.Load(); n != cached {
			t.Fatalf("cached connects made %d decision round trips, want 0", n-cached)
		}
		owner.addGroupMember(owner.createGroup("unrelated"), store.GroupMemberUser, owner.id)
		ztEventually(t, 5*time.Second, "decision after group change flush", func() bool {
			got, err := tcpRoundTrip(listen, "flushed")
			return err == nil && got == "flushed" && s.connect.decisions.Load() > cached
		})
		if n := s.connect.decisions.Load(); n != cached+1 {
			t.Fatalf("after flush got %d decision round trips, want 1", n-cached)
		}
	})

	t.Run("leaving the group drops the live tcp stream", func(t *testing.T) {
		temp := owner.invite("temp@example.com")
		owner.addGroupMember(group, store.GroupMemberUser, temp.id)
		listen := freeAddr(t, "tcp")
		s.runUserAgent(owner, temp, "temp-laptop", "", []agent.ConnectSpec{{Name: "pg", Endpoint: "postgres", Kind: store.KindTCP, Listen: listen}})
		ztAwaitTCP(t, listen, 8*time.Second)
		stream := ztLiveStream(t, listen)

		owner.mustOK("DELETE", "/api/v1/groups/"+group+"/members/"+store.GroupMemberUser+"/"+temp.id, nil)

		ztAwaitDropped(t, stream, "after leaving the group")
		ztRefusedTCP(t, listen)
	})

	t.Run("revoking the user drops live tcp stream and udp flow", func(t *testing.T) {
		tcpListen := freeAddr(t, "tcp")
		udpListen := freeAddr(t, "udp")
		s.runUserAgent(owner, ops, "ops-laptop", "", []agent.ConnectSpec{
			{Name: "pg", Endpoint: "postgres", Kind: store.KindTCP, Listen: tcpListen},
			{Name: "tm", Endpoint: "telemetry", Kind: store.KindUDP, Listen: udpListen, Idle: 30 * time.Second},
		})
		ztAwaitTCP(t, tcpListen, 8*time.Second)
		stream, err := net.DialTimeout("tcp", tcpListen, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		buf := make([]byte, 64)
		if _, err := stream.Write([]byte("live")); err != nil {
			t.Fatal(err)
		}
		_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
		if n, err := stream.Read(buf); err != nil || string(buf[:n]) != "live" {
			t.Fatalf("live stream: got %q, err %v", buf[:n], err)
		}
		flow := awaitUDPEcho(t, udpListen, "flow-up")

		owner.mustOK("PATCH", "/api/v1/members/"+ops.id, map[string]any{"disabled": true})

		_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := stream.Read(buf)
		var ne net.Error
		switch {
		case err == nil:
			t.Fatalf("live stream still delivering after revoke: %q", buf[:n])
		case errors.As(err, &ne) && ne.Timeout():
			t.Fatal("live tcp stream not dropped after revoke")
		}

		ztEventually(t, 5*time.Second, "udp flow dropped", func() bool {
			_, err := udpRoundTrip(flow, "after-revoke", 300*time.Millisecond)
			return err != nil
		})
		for i := range 3 {
			if got, err := udpRoundTrip(flow, "still-there", 300*time.Millisecond); err == nil {
				t.Fatalf("udp flow answered after revoke (attempt %d): %q", i, got)
			}
		}
		fresh, err := net.Dial("udp", udpListen)
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		if got, err := udpRoundTrip(fresh, "new-flow", 500*time.Millisecond); err == nil {
			t.Fatalf("new udp flow answered after revoke: %q", got)
		}
		ztRefusedTCP(t, tcpListen)
	})

	t.Run("decision service unavailable fails closed", func(t *testing.T) {
		listen := freeAddr(t, "tcp")
		s.runUserAgent(owner, dba, "outage-probe", "", []agent.ConnectSpec{{Name: "pg", Endpoint: "postgres", Kind: store.KindTCP, Listen: listen}})
		ztAwaitTCP(t, listen, 8*time.Second)
		s.deciders.down.Store(true)
		t.Cleanup(func() { s.deciders.down.Store(false) })
		ztRefusedTCP(t, listen)
		s.deciders.down.Store(false)
		ztAwaitTCP(t, listen, 5*time.Second)
	})

	t.Run("control disconnect fails closed", func(t *testing.T) {
		relay := newZTRelay(t, strings.TrimPrefix(s.api.URL, "http://"))
		listen := freeAddr(t, "tcp")
		s.runUserAgent(owner, dba, "flaky-link", relay.url(), []agent.ConnectSpec{{Name: "pg", Endpoint: "postgres", Kind: store.KindTCP, Listen: listen}})
		ztAwaitTCP(t, listen, 8*time.Second)
		relay.cut()
		ztRefusedTCP(t, listen)
		time.Sleep(500 * time.Millisecond)
		ztRefusedTCP(t, listen)
		relay.restore()
		ztAwaitTCP(t, listen, 40*time.Second)
	})
}

func ztLiveStream(t *testing.T, addr string) net.Conn {
	t.Helper()
	stream, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	buf := make([]byte, 64)
	if _, err := stream.Write([]byte("live")); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := stream.Read(buf); err != nil || string(buf[:n]) != "live" {
		t.Fatalf("live stream: got %q, err %v", buf[:n], err)
	}
	return stream
}

func ztAwaitDropped(t *testing.T, stream net.Conn, when string) {
	t.Helper()
	buf := make([]byte, 64)
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := stream.Read(buf)
	var ne net.Error
	switch {
	case err == nil:
		t.Fatalf("live stream still delivering %s: %q", when, buf[:n])
	case errors.As(err, &ne) && ne.Timeout():
		t.Fatalf("live stream not dropped %s", when)
	}
}
