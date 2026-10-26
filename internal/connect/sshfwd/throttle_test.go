package sshfwd

import (
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func clientConfigUser(user, pass string) *ssh.ClientConfig {
	cfg := clientConfig(pass)
	cfg.User = user
	return cfg
}

func TestSuccessDoesNotClearFailures(t *testing.T) {
	_, ln, _, token := startServer(t, Options{AuthFailureLimit: 3, AuthFailureWindow: time.Minute})
	addr := ln.Addr().String()
	for i := 0; i < 2; i++ {
		if _, err := ssh.Dial("tcp", addr, clientConfig("wrong")); err == nil {
			t.Fatal("expected auth failure")
		}
	}
	c, err := ssh.Dial("tcp", addr, clientConfig(token))
	if err != nil {
		t.Fatalf("valid token rejected below the limit: %v", err)
	}
	_ = c.Close()
	if _, err := ssh.Dial("tcp", addr, clientConfig("wrong")); err == nil {
		t.Fatal("expected auth failure")
	}
	if c, err := ssh.Dial("tcp", addr, clientConfig(token)); err == nil {
		_ = c.Close()
		t.Fatal("a successful login cleared the failure window")
	}
}

func TestFailuresForOneUserDoNotBlockAnother(t *testing.T) {
	_, ln, _, token := startServer(t, Options{AuthFailureLimit: 3, AuthFailureWindow: time.Minute})
	addr := ln.Addr().String()
	for i := 0; i < 3; i++ {
		if _, err := ssh.Dial("tcp", addr, clientConfigUser("alice", "wrong")); err == nil {
			t.Fatal("expected auth failure")
		}
	}
	if _, err := ssh.Dial("tcp", addr, clientConfigUser("alice", token)); err == nil {
		t.Fatal("alice should be throttled")
	}
	c, err := ssh.Dial("tcp", addr, clientConfigUser("bob", token))
	if err != nil {
		t.Fatalf("bob blocked by alice's failures: %v", err)
	}
	_ = c.Close()
}

func TestIdentitySprayingHitsLooserIPCap(t *testing.T) {
	th := newFailureThrottle(3, time.Minute)
	for i := 0; i < 3*ipFailureMultiplier; i++ {
		th.fail("203.0.113.9", string(rune('a'+i)))
	}
	if !th.blocked("203.0.113.9", "fresh-user") {
		t.Fatal("per-IP cap did not stop identity spraying")
	}
	if th.blocked("203.0.113.10", "fresh-user") {
		t.Fatal("other IP was blocked")
	}
}

func TestRemoteIPRejectsUnusableAddresses(t *testing.T) {
	if _, ok := remoteIP(nil); ok {
		t.Fatal("nil address accepted")
	}
	if _, ok := remoteIP(&net.UnixAddr{Name: "@sock", Net: "unix"}); ok {
		t.Fatal("unparsable address accepted")
	}
	if ip, ok := remoteIP(&net.TCPAddr{IP: net.ParseIP("::ffff:10.0.0.1"), Port: 22}); !ok || ip != "10.0.0.1" {
		t.Fatalf("got %q %v", ip, ok)
	}
}
