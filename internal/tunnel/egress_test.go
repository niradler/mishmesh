package tunnel

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEgressReplyRoundTrip(t *testing.T) {
	for _, reply := range []EgressReply{
		{Status: EgressOK},
		{Status: EgressDenied, Reason: "policy_deny"},
		{Status: EgressDialFailed, Reason: strings.Repeat("x", 300)},
	} {
		var buf bytes.Buffer
		if err := WriteEgressReply(&buf, reply); err != nil {
			t.Fatal(err)
		}
		buf.WriteString("payload")
		got, err := ReadEgressReply(&buf)
		if err != nil {
			t.Fatal(err)
		}
		want := reply
		if len(want.Reason) > maxEgressReason {
			want.Reason = want.Reason[:maxEgressReason]
		}
		if got != want || buf.String() != "payload" {
			t.Fatalf("got %+v rest %q, want %+v", got, buf.String(), want)
		}
	}
}

func TestEgressReplyRejectsGarbage(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("HTTP/1.1 200 OK\r\n"),
		append(append([]byte{}, egressReplyMagic...), 0x7f, 0),
		append(append([]byte{}, egressReplyMagic...), 0, 250),
	} {
		if _, err := ReadEgressReply(bytes.NewReader(raw)); !errors.Is(err, ErrBadEgressReply) {
			t.Fatalf("%q: err = %v", raw, err)
		}
	}
	if _, err := ReadEgressReply(bytes.NewReader(egressReplyMagic[:3])); err == nil {
		t.Fatal("short reply accepted")
	}
}

func TestExitTargetsRoundTrip(t *testing.T) {
	want := []string{"10.0.0.5:443", "[2001:db8::1]:443"}
	var buf bytes.Buffer
	if err := WriteExitTargets(&buf, want); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("payload")
	got, err := ReadExitTargets(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") || buf.String() != "payload" {
		t.Fatalf("got %v rest %q", got, buf.String())
	}
}

func TestExitResolutionSeesRefusal(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteEgressReply(&buf, EgressReply{Status: EgressDenied, Reason: "blocked_destination"}); err != nil {
		t.Fatal(err)
	}
	targets, reply, err := ReadExitResolution(&buf)
	if err != nil || targets != nil || reply == nil || reply.Status != EgressDenied || reply.Reason != "blocked_destination" {
		t.Fatalf("targets %v reply %+v err %v", targets, reply, err)
	}
	if err := WriteEgressReply(&buf, EgressReply{Status: EgressOK}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExitTargets(&buf); !errors.Is(err, ErrBadExitTargets) {
		t.Fatalf("reply read as targets: %v", err)
	}
}

func TestExitTargetsRejectsMalformed(t *testing.T) {
	tooMany := make([]string, MaxExitTargets+1)
	for i := range tooMany {
		tooMany[i] = "1.2.3.4:1"
	}
	for _, targets := range [][]string{tooMany, {""}, {strings.Repeat("x", maxExitTargetBytes+1)}} {
		if err := WriteExitTargets(io.Discard, targets); !errors.Is(err, ErrBadExitTargets) {
			t.Fatalf("write %d targets: err = %v", len(targets), err)
		}
	}
	for _, raw := range [][]byte{
		[]byte("HTTP/1.1 200 OK\r\n"),
		append(append([]byte{}, exitTargetsMagic...), MaxExitTargets+1),
		append(append([]byte{}, exitTargetsMagic...), 1, 0),
		append(append([]byte{}, exitTargetsMagic...), 1, maxExitTargetBytes+1),
	} {
		if _, err := ReadExitTargets(bytes.NewReader(raw)); !errors.Is(err, ErrBadExitTargets) {
			t.Fatalf("%q: err = %v", raw, err)
		}
	}
}
