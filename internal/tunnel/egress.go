package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	MetaEgress       = "egress"
	MetaEgressAuth   = "egress_auth"
	MetaEgressVia    = "egress_via"
	MetaEgressExit   = "egress_exit"
	MetaEgressClient = "egress_client"

	EgressReplyTimeout = 15 * time.Second
	maxEgressReason    = 200
)

type EgressStatus byte

const (
	EgressOK EgressStatus = iota
	EgressDenied
	EgressUnavailable
	EgressDialFailed
	EgressInvalid
	EgressAuthRequired
)

func (s EgressStatus) String() string {
	switch s {
	case EgressOK:
		return "ok"
	case EgressDenied:
		return "denied"
	case EgressUnavailable:
		return "unavailable"
	case EgressDialFailed:
		return "dial_failed"
	case EgressInvalid:
		return "invalid"
	case EgressAuthRequired:
		return "auth_required"
	}
	return "unknown"
}

var (
	egressReplyMagic  = []byte{0x00, 'M', 'M', 'E', 'G', 0x01}
	ErrBadEgressReply = errors.New("tunnel: malformed egress reply")
)

type EgressReply struct {
	Status EgressStatus
	Reason string
}

func WriteEgressReply(w io.Writer, reply EgressReply) error {
	reason := reply.Reason
	if len(reason) > maxEgressReason {
		reason = reason[:maxEgressReason]
	}
	frame := make([]byte, 0, len(egressReplyMagic)+2+len(reason))
	frame = append(frame, egressReplyMagic...)
	frame = append(frame, byte(reply.Status), byte(len(reason)))
	frame = append(frame, reason...)
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("write egress reply: %w", err)
	}
	return nil
}

func ReadEgressReply(r io.Reader) (EgressReply, error) {
	head := make([]byte, len(egressReplyMagic)+2)
	if _, err := io.ReadFull(r, head); err != nil {
		return EgressReply{}, fmt.Errorf("read egress reply: %w", err)
	}
	if !bytes.Equal(head[:len(egressReplyMagic)], egressReplyMagic) {
		return EgressReply{}, ErrBadEgressReply
	}
	status := EgressStatus(head[len(egressReplyMagic)])
	n := int(head[len(egressReplyMagic)+1])
	if status > EgressAuthRequired || n > maxEgressReason {
		return EgressReply{}, ErrBadEgressReply
	}
	reason := make([]byte, n)
	if _, err := io.ReadFull(r, reason); err != nil {
		return EgressReply{}, fmt.Errorf("read egress reply: %w", err)
	}
	return EgressReply{Status: status, Reason: string(reason)}, nil
}
