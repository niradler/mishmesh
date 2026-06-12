package tunnel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	ProtocolVersion = 1
	maxFrameSize    = 1 << 20
)

type MsgType string

const (
	MsgRegister    MsgType = "register"
	MsgRegisterAck MsgType = "register_ack"
	MsgPing        MsgType = "ping"
	MsgPong        MsgType = "pong"
	MsgError       MsgType = "error"
)

type StreamInit struct {
	EndpointID string            `json:"endpoint_id"`
	Kind       string            `json:"kind"`
	Meta       map[string]string `json:"meta,omitempty"`
}

type EndpointRequest struct {
	Ref       string `json:"ref"`
	Kind      string `json:"kind"`
	Lifecycle string `json:"lifecycle"`
	Subdomain string `json:"subdomain,omitempty"`
}

type EndpointBinding struct {
	Ref        string `json:"ref"`
	EndpointID string `json:"endpoint_id"`
	PublicURL  string `json:"public_url"`
	Kind       string `json:"kind"`
}

type RegisterPayload struct {
	Endpoints []EndpointRequest `json:"endpoints"`
}

type RegisterAckPayload struct {
	Endpoints []EndpointBinding `json:"endpoints"`
}

type ControlMessage struct {
	Type        MsgType             `json:"type"`
	Register    *RegisterPayload    `json:"register,omitempty"`
	RegisterAck *RegisterAckPayload `json:"register_ack,omitempty"`
	Error       string              `json:"error,omitempty"`
}

func writeFrame(w io.Writer, p []byte) error {
	if len(p) > maxFrameSize {
		return fmt.Errorf("tunnel: frame too large: %d > %d", len(p), maxFrameSize)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(p)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameSize {
		return nil, fmt.Errorf("tunnel: frame too large: %d > %d", n, maxFrameSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, b)
}

func readJSON(r io.Reader, v any) error {
	b, err := readFrame(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
