package tunnel

import (
	"io"
	"net"
	"time"
	"unicode/utf8"
)

const TLSHandshakeRecord = 0x16

func WriteWithDeadline(c net.Conn, timeout time.Duration, write func() error) error {
	_ = c.SetWriteDeadline(time.Now().Add(timeout))
	err := write()
	_ = c.SetWriteDeadline(time.Time{})
	return err
}

func ReadWithDeadline[T any](c net.Conn, timeout time.Duration, read func() (T, error)) (T, error) {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	v, err := read()
	_ = c.SetReadDeadline(time.Time{})
	return v, err
}

func TruncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

type BufferedConn struct {
	net.Conn
	R io.Reader
}

func (c *BufferedConn) Read(p []byte) (int, error) {
	return c.R.Read(p)
}

func (c *BufferedConn) CloseWrite() error {
	return CloseWrite(c.Conn)
}
