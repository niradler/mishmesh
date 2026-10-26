package sshfwd

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/ssh"
)

type recordingChannel struct {
	ssh.Channel
	buf bytes.Buffer
}

func (r *recordingChannel) Write(p []byte) (int, error) { return r.buf.Write(p) }

func newBareAgentConn() *agentConn {
	return &agentConn{
		forwards: make(map[string]forwardSpec),
		sessions: make(map[ssh.Channel]struct{}),
	}
}

func TestCancelledForwardURLNotReplayedToNewSessions(t *testing.T) {
	a := newBareAgentConn()
	a.setForward("ep_1", "0.0.0.0", 80)
	a.announce("ep_1", "http://one.example")
	a.dropForward("ep_1")

	ch := &recordingChannel{}
	a.addSession(ch)
	if ch.buf.Len() != 0 {
		t.Fatalf("stale url replayed to new session: %q", ch.buf.String())
	}
}

func TestLiveForwardURLReplayedToNewSessions(t *testing.T) {
	a := newBareAgentConn()
	a.setForward("ep_1", "0.0.0.0", 80)
	a.announce("ep_1", "http://one.example")

	ch := &recordingChannel{}
	a.addSession(ch)
	if got := ch.buf.String(); got != "  http://one.example\r\n" {
		t.Fatalf("replay = %q", got)
	}
}
