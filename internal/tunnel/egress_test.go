package tunnel

import (
	"bytes"
	"errors"
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
