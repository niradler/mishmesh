package cluster

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
)

const (
	StatusOK      byte = 0
	StatusNotHere byte = 1
	StatusError   byte = 2

	MaxHeaderBytes = 16 * 1024
	MaxClockSkew   = 60 * time.Second
	MinSecretLen   = 32
)

var (
	ErrNotHere      = errors.New("cluster: agent not connected to this node")
	ErrRelayFailed  = errors.New("cluster: relay failed to open stream")
	ErrBadSignature = errors.New("cluster: bad header signature")
	ErrStaleHeader  = errors.New("cluster: header timestamp outside allowed skew")
	ErrHeaderTooBig = errors.New("cluster: header too large")
)

type Header struct {
	AgentID    string            `json:"agent_id"`
	EndpointID string            `json:"endpoint_id"`
	Kind       string            `json:"kind"`
	Meta       map[string]string `json:"meta,omitempty"`
	Timestamp  int64             `json:"ts"`
	MAC        string            `json:"mac"`
}

func (h Header) canonical() []byte {
	var buf []byte
	appendField := func(s string) {
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		buf = append(buf, s...)
	}
	appendField(h.AgentID)
	appendField(h.EndpointID)
	appendField(h.Kind)
	appendField(strconv.FormatInt(h.Timestamp, 10))
	keys := make([]string, 0, len(h.Meta))
	for k := range h.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf = binary.AppendUvarint(buf, uint64(len(keys)))
	for _, k := range keys {
		appendField(k)
		appendField(h.Meta[k])
	}
	return buf
}

func (h Header) computeMAC(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(h.canonical())
	return m.Sum(nil)
}

func (h *Header) Sign(secret []byte) {
	h.MAC = hex.EncodeToString(h.computeMAC(secret))
}

func (h Header) Verify(secret []byte, now time.Time) error {
	got, err := hex.DecodeString(h.MAC)
	if err != nil {
		return ErrBadSignature
	}
	if !hmac.Equal(got, h.computeMAC(secret)) {
		return ErrBadSignature
	}
	skew := now.Sub(time.Unix(h.Timestamp, 0))
	if skew > MaxClockSkew || skew < -MaxClockSkew {
		return ErrStaleHeader
	}
	return nil
}

func WriteHeader(w io.Writer, h Header) error {
	body, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("cluster: marshal header: %w", err)
	}
	if len(body) > MaxHeaderBytes {
		return ErrHeaderTooBig
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("cluster: write header: %w", err)
	}
	return nil
}

func ReadHeader(r io.Reader) (Header, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return Header{}, fmt.Errorf("cluster: read header length: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > MaxHeaderBytes {
		return Header{}, ErrHeaderTooBig
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Header{}, fmt.Errorf("cluster: read header: %w", err)
	}
	var h Header
	if err := json.Unmarshal(body, &h); err != nil {
		return Header{}, fmt.Errorf("cluster: decode header: %w", err)
	}
	return h, nil
}
