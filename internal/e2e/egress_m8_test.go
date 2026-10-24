package e2e

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

func (h *egressHarness) serviceAccountToken(orgID, name string) (string, string) {
	h.t.Helper()
	sa := &store.ServiceAccount{ID: store.NewID("sa"), OrgID: orgID, Name: name, Role: string(authz.RoleMember), ClientID: store.NewID("cid"), SecretHash: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := h.data.CreateServiceAccount(h.ctx, sa); err != nil {
		h.t.Fatal(err)
	}
	issued, err := h.ids.Issue(h.ctx, identity.IssueRequest{
		OrgID: orgID, Subject: authz.EntityRef{Type: authz.EntityServiceAccount, ID: sa.ID},
		Type: token.TypeAccess, AuthMethod: authz.AuthMethodClientCredentials, TTL: time.Hour,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return sa.ID, issued.Token
}

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func loopbackExitDialer(t *testing.T) agent.ExitDialer {
	t.Helper()
	guard, err := proxy.NewGuard(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

func assertEcho(t *testing.T, c net.Conn, r io.Reader) {
	t.Helper()
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 4)
	if _, err := io.ReadFull(r, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

func TestEgressViaExitAgent(t *testing.T) {
	h := newEgressHarness(t, false)
	echo := tcpEchoServer(t)
	orgID := h.orgWithPolicy(egressPolicyFor(echo))
	h.runNamedAgent(orgID, "exit-a", agent.EgressSpec{ServeExit: true}, loopbackExitDialer(t))
	listen := h.runNamedAgent(orgID, "src", agent.EgressSpec{Listen: freeAddr(t, "tcp"), Exit: "exit-a"}, nil)

	c, br, status := proxyConnect(t, listen, echo)
	if status != http.StatusOK {
		t.Fatalf("CONNECT via exit status = %d, want 200", status)
	}
	assertEcho(t, c, br)
	_ = c.Close()
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == echo })
	if ev.Decision != store.AccessAllow || ev.ExitAgentID == "" || ev.ExitAgentID == ev.AgentID {
		t.Fatalf("access event = %+v", ev)
	}
}

func TestEgressExitAgentMustOptIn(t *testing.T) {
	h := newEgressHarness(t, false)
	echo := tcpEchoServer(t)
	orgID := h.orgWithPolicy(egressPolicyFor(echo))
	h.runNamedAgent(orgID, "plain", agent.EgressSpec{Listen: freeAddr(t, "tcp")}, loopbackExitDialer(t))
	listen := h.runNamedAgent(orgID, "src", agent.EgressSpec{Listen: freeAddr(t, "tcp"), Exit: "plain"}, nil)

	if _, _, status := proxyConnect(t, listen, echo); status != http.StatusForbidden {
		t.Fatalf("CONNECT via non-exit agent status = %d, want 403", status)
	}
}

func TestEgressExitAgentInOtherOrgIsUnknown(t *testing.T) {
	h := newEgressHarness(t, false)
	echo := tcpEchoServer(t)
	h.runNamedAgent(h.orgWithPolicy(egressPolicyFor(echo)), "foreign-exit", agent.EgressSpec{ServeExit: true}, loopbackExitDialer(t))
	listen := h.runNamedAgent(h.orgWithPolicy(egressPolicyFor(echo)), "src", agent.EgressSpec{Listen: freeAddr(t, "tcp"), Exit: "foreign-exit"}, nil)

	if _, _, status := proxyConnect(t, listen, echo); status != http.StatusForbidden {
		t.Fatalf("CONNECT via foreign exit status = %d, want 403", status)
	}
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == echo })
	if ev.Decision != store.AccessDeny || ev.Reason != "unknown_exit" {
		t.Fatalf("access event = %+v", ev)
	}
}

func TestEgressSharedGatewayAttributesEachCaller(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	orgID := h.orgWithPolicy(egressPolicyFor(echo))
	listen := h.runNamedAgent(orgID, "shared", agent.EgressSpec{Listen: freeAddr(t, "tcp"), RequireAuth: true}, nil)
	saA, rawA := h.serviceAccountToken(orgID, "build")
	saB, rawB := h.serviceAccountToken(orgID, "deploy")

	for _, auth := range []string{"Bearer " + rawA, basicAuth("deploy", rawB)} {
		c, br, resp := proxyConnectAuth(t, listen, echo, auth)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
		}
		assertEcho(t, c, br)
		_ = c.Close()
	}
	for _, id := range []string{saA, saB} {
		ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.PrincipalID == id })
		if ev.Decision != store.AccessAllow || ev.PrincipalType != string(authz.EntityServiceAccount) {
			t.Fatalf("access event for %s = %+v", id, ev)
		}
	}
}

func TestEgressRequireAuthChallenges(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	orgID := h.orgWithPolicy(egressPolicyFor(echo))
	listen := h.runNamedAgent(orgID, "shared", agent.EgressSpec{Listen: freeAddr(t, "tcp"), RequireAuth: true}, nil)

	for _, auth := range []string{"", "Digest abc", "Bearer not-a-token"} {
		_, _, resp := proxyConnectAuth(t, listen, echo, auth)
		if resp.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("auth %q: status = %d, want 407", auth, resp.StatusCode)
		}
		if resp.Header.Get("Proxy-Authenticate") == "" {
			t.Fatalf("auth %q: missing Proxy-Authenticate challenge", auth)
		}
	}
}

func TestEgressRevokedProxyTokenRefused(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	orgID := h.orgWithPolicy(egressPolicyFor(echo))
	listen := h.runNamedAgent(orgID, "shared", agent.EgressSpec{Listen: freeAddr(t, "tcp"), RequireAuth: true}, nil)
	saID, raw := h.serviceAccountToken(orgID, "build")

	c, br, resp := proxyConnectAuth(t, listen, echo, "Bearer "+raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}
	assertEcho(t, c, br)
	if err := h.ids.RevokeSubject(h.ctx, orgID, authz.EntityRef{Type: authz.EntityServiceAccount, ID: saID}); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("open stream not closed after revocation: %v", err)
	}
	if _, _, resp := proxyConnectAuth(t, listen, echo, "Bearer "+raw); resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("revoked token CONNECT status = %d, want 407", resp.StatusCode)
	}
}

func socksConnect(t *testing.T, addr, target, user, pass string) (net.Conn, *bufio.Reader, byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, br, code, err := socksDial(addr, target, user, pass)
		if err == nil && !(code == 0x01 && time.Now().Before(deadline)) {
			t.Cleanup(func() { _ = c.Close() })
			return c, br, code
		}
		if c != nil {
			_ = c.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("socks %s never answered: %v", addr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func socksDial(addr, target, user, pass string) (net.Conn, *bufio.Reader, byte, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, nil, 0, err
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	method := byte(0x00)
	if user != "" || pass != "" {
		method = 0x02
	}
	if _, err := c.Write([]byte{0x05, 0x01, method}); err != nil {
		return c, nil, 0, err
	}
	sel := make([]byte, 2)
	if _, err := io.ReadFull(br, sel); err != nil {
		return c, nil, 0, err
	}
	if sel[1] == 0xff {
		return c, br, 0xff, nil
	}
	if sel[1] == 0x02 {
		msg := append([]byte{0x01, byte(len(user))}, user...)
		msg = append(append(msg, byte(len(pass))), pass...)
		if _, err := c.Write(msg); err != nil {
			return c, nil, 0, err
		}
		status := make([]byte, 2)
		if _, err := io.ReadFull(br, status); err != nil {
			return c, nil, 0, err
		}
		if status[1] != 0x00 {
			return c, br, 0xfe, nil
		}
	}
	host, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)
	req := append([]byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return c, nil, 0, err
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(br, reply); err != nil {
		return c, nil, 0, err
	}
	_ = c.SetDeadline(time.Time{})
	return c, br, reply[1], nil
}

func TestEgressSocks5(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	other := tcpEchoServer(t)
	orgID := h.orgWithPolicy(egressPolicyFor(echo))
	socks := h.runNamedAgent(orgID, "socks", agent.EgressSpec{SocksListen: freeAddr(t, "tcp")}, nil)
	locked := h.runNamedAgent(orgID, "locked", agent.EgressSpec{SocksListen: freeAddr(t, "tcp"), RequireAuth: true}, nil)

	c, br, code := socksConnect(t, socks, echo, "", "")
	if code != 0x00 {
		t.Fatalf("socks reply = %#x, want success", code)
	}
	assertEcho(t, c, br)
	_ = c.Close()
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == echo })
	if ev.Decision != store.AccessAllow {
		t.Fatalf("access event = %+v", ev)
	}

	if _, _, code := socksConnect(t, socks, other, "", ""); code != 0x02 {
		t.Fatalf("denied socks reply = %#x, want 0x02", code)
	}
	if _, _, code := socksConnect(t, locked, echo, "", ""); code != 0xff {
		t.Fatalf("unauthenticated socks method = %#x, want 0xff", code)
	}
}

func TestEgressPolicyChangeClosesLiveTunnel(t *testing.T) {
	h := newEgressHarness(t, true)
	echo := tcpEchoServer(t)
	orgID := h.orgWithPolicy(egressPolicyFor(echo))
	listen := h.runAgent(orgID)
	c, br, status := proxyConnect(t, listen, echo)
	if status != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", status)
	}
	assertEcho(t, c, br)

	if err := h.data.SetOrgPolicy(h.ctx, &store.OrgPolicy{OrgID: orgID, CedarSrc: egressPolicyFor("127.0.0.1:1"), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h.ids.FlushAll()

	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("tunnel still delivering after the policy stopped allowing it")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("live egress tunnel not closed after the policy change")
	}
	if _, _, status := proxyConnect(t, listen, echo); status == http.StatusOK {
		t.Fatal("new CONNECT allowed after the policy change")
	}
}

func TestEgressViaExitPolicySeesExitResolvedIP(t *testing.T) {
	echo := tcpEchoServer(t)
	_, port, _ := net.SplitHostPort(echo)
	byName := net.JoinHostPort("localhost", port)
	cases := []struct {
		name   string
		policy string
		want   int
		reason authz.Reason
	}{
		{"permit on exit resolved loopback", fmt.Sprintf(`permit (principal, action == Action::"egress:connect", resource) when { resource.port == %s && context has exit && context has resolved_ip && context.resolved_ip.isLoopback() };`, port), http.StatusOK, authz.ReasonAllow},
		{"permit only on another range", fmt.Sprintf(`permit (principal, action == Action::"egress:connect", resource) when { resource.port == %s && context has resolved_ip && context.resolved_ip.isInRange(ip("10.0.0.0/8")) };`, port), http.StatusForbidden, authz.ReasonNoPolicyMatch},
		{"forbid on exit resolved loopback", fmt.Sprintf(`permit (principal, action == Action::"egress:connect", resource) when { resource.port == %s };
forbid (principal, action == Action::"egress:connect", resource) when { context has resolved_ip && context.resolved_ip.isLoopback() };`, port), http.StatusForbidden, authz.ReasonPolicyDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newEgressHarness(t, false)
			orgID := h.orgWithPolicy(tc.policy)
			h.runNamedAgent(orgID, "exit-a", agent.EgressSpec{ServeExit: true}, loopbackExitDialer(t))
			listen := h.runNamedAgent(orgID, "src", agent.EgressSpec{Listen: freeAddr(t, "tcp"), Exit: "exit-a"}, nil)
			c, br, status := proxyConnect(t, listen, byName)
			if status != tc.want {
				t.Fatalf("CONNECT %s via exit status = %d, want %d", byName, status, tc.want)
			}
			if status == http.StatusOK {
				assertEcho(t, c, br)
				_ = c.Close()
			}
			ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == byName })
			if ev.ExitAgentID == "" || ev.Reason != string(tc.reason) {
				t.Fatalf("access event = %+v, want exit agent and reason %s", ev, tc.reason)
			}
		})
	}
}

func TestEgressViaExitRefusesExitBlockedAnswer(t *testing.T) {
	h := newEgressHarness(t, false)
	echo := tcpEchoServer(t)
	orgID := h.orgWithPolicy(`permit (principal, action == Action::"egress:connect", resource);`)
	h.runNamedAgent(orgID, "exit-a", agent.EgressSpec{ServeExit: true}, proxy.DefaultGuard())
	listen := h.runNamedAgent(orgID, "src", agent.EgressSpec{Listen: freeAddr(t, "tcp"), Exit: "exit-a"}, nil)

	if _, _, status := proxyConnect(t, listen, echo); status != http.StatusForbidden {
		t.Fatalf("CONNECT to an address the exit blocks = %d, want 403", status)
	}
	ev := h.access.waitFor(t, func(e store.AccessEvent) bool { return e.Destination == echo })
	if ev.Decision != store.AccessDeny || ev.Reason != "exit_blocked_destination" {
		t.Fatalf("access event = %+v", ev)
	}
}
