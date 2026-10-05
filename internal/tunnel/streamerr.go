package tunnel

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const maxStreamErrorReason = 200

var streamErrorMagic = []byte{0x00, 'M', 'M', 'E', 'R', 'R', 0x01}

func WriteStreamError(w io.Writer, reason string) error {
	if len(reason) > maxStreamErrorReason {
		reason = reason[:maxStreamErrorReason]
	}
	frame := make([]byte, 0, len(streamErrorMagic)+2+len(reason))
	frame = append(frame, streamErrorMagic...)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(reason)))
	frame = append(frame, reason...)
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("write stream error: %w", err)
	}
	return nil
}

func ReadStreamError(br *bufio.Reader) (string, bool) {
	head, err := br.Peek(len(streamErrorMagic))
	if err != nil || !bytes.Equal(head, streamErrorMagic) {
		return "", false
	}
	_, _ = br.Discard(len(streamErrorMagic))
	var lenBuf [2]byte
	if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
		return "", true
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	if n > maxStreamErrorReason {
		return "", true
	}
	reason := make([]byte, n)
	if _, err := io.ReadFull(br, reason); err != nil {
		return "", true
	}
	return string(reason), true
}
