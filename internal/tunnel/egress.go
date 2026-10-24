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
	MetaExitResolve  = "egress_exit_resolve"

	EgressReplyTimeout = 15 * time.Second
	maxEgressReason    = 200
	MaxExitTargets     = 16
	maxExitTargetBytes = 64
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
	exitTargetsMagic  = []byte{0x00, 'M', 'M', 'E', 'R', 0x01}
	ErrBadEgressReply = errors.New("tunnel: malformed egress reply")
	ErrBadExitTargets = errors.New("tunnel: malformed exit targets")
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

func WriteExitTargets(w io.Writer, targets []string) error {
	if len(targets) > MaxExitTargets {
		return ErrBadExitTargets
	}
	frame := make([]byte, 0, len(exitTargetsMagic)+1+len(targets)*(maxExitTargetBytes+1))
	frame = append(frame, exitTargetsMagic...)
	frame = append(frame, byte(len(targets)))
	for _, t := range targets {
		if t == "" || len(t) > maxExitTargetBytes {
			return ErrBadExitTargets
		}
		frame = append(frame, byte(len(t)))
		frame = append(frame, t...)
	}
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("write exit targets: %w", err)
	}
	return nil
}

func ReadExitTargets(r io.Reader) ([]string, error) {
	targets, reply, err := ReadExitResolution(r)
	if err != nil {
		return nil, err
	}
	if reply != nil {
		return nil, ErrBadExitTargets
	}
	return targets, nil
}

func ReadExitResolution(r io.Reader) ([]string, *EgressReply, error) {
	magic := make([]byte, len(exitTargetsMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, nil, fmt.Errorf("read exit resolution: %w", err)
	}
	if bytes.Equal(magic, egressReplyMagic) {
		reply, err := ReadEgressReply(io.MultiReader(bytes.NewReader(magic), r))
		if err != nil {
			return nil, nil, err
		}
		return nil, &reply, nil
	}
	if !bytes.Equal(magic, exitTargetsMagic) {
		return nil, nil, ErrBadExitTargets
	}
	var count [1]byte
	if _, err := io.ReadFull(r, count[:]); err != nil {
		return nil, nil, fmt.Errorf("read exit targets: %w", err)
	}
	if int(count[0]) > MaxExitTargets {
		return nil, nil, ErrBadExitTargets
	}
	targets := make([]string, 0, count[0])
	for range int(count[0]) {
		var n [1]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return nil, nil, fmt.Errorf("read exit targets: %w", err)
		}
		if n[0] == 0 || int(n[0]) > maxExitTargetBytes {
			return nil, nil, ErrBadExitTargets
		}
		t := make([]byte, n[0])
		if _, err := io.ReadFull(r, t); err != nil {
			return nil, nil, fmt.Errorf("read exit targets: %w", err)
		}
		targets = append(targets, string(t))
	}
	return targets, nil, nil
}
