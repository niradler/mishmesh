package egress

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func clientHelloBytes(t *testing.T, serverName string) []byte {
	t.Helper()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		tc := tls.Client(client, &tls.Config{ServerName: serverName, InsecureSkipVerify: serverName == ""})
		_ = tc.SetDeadline(time.Now().Add(2 * time.Second))
		_ = tc.Handshake()
		_ = client.Close()
	}()
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(server, hdr); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, int(hdr[3])<<8|int(hdr[4]))
	if _, err := io.ReadFull(server, body); err != nil {
		t.Fatal(err)
	}
	return append(hdr, body...)
}

func feed(t *testing.T, host string, payload []byte) (*sniConn, []byte, error) {
	t.Helper()
	near, far := net.Pipe()
	t.Cleanup(func() { _ = near.Close() })
	go func() {
		_, _ = far.Write(payload)
		_ = far.Close()
	}()
	c := newSNIConn(near, host)
	got, err := io.ReadAll(c)
	return c, got, err
}

func TestSNIConn(t *testing.T) {
	hello := clientHelloBytes(t, "api.example.com")
	tests := []struct {
		name     string
		host     string
		payload  []byte
		wantErr  error
		mismatch string
	}{
		{name: "matching sni replays hello", host: "api.example.com", payload: hello},
		{name: "matching sni ignores case and trailing dot", host: "api.example.com", payload: clientHelloBytes(t, "API.Example.com.")},
		{name: "no sni to an ip passes", host: "93.184.216.34", payload: clientHelloBytes(t, "")},
		{name: "no sni to a hostname is a mismatch", host: "cdn.example.com", payload: clientHelloBytes(t, ""), wantErr: ErrSNIMismatch, mismatch: reasonSNI},
		{name: "mismatched sni", host: "93.184.216.34", payload: hello, wantErr: ErrSNIMismatch, mismatch: reasonSNI},
		{name: "grease ech with a matching outer name passes", host: "api.example.com", payload: withExtension(t, hello, extensionECH, echOuterPayload)},
		{name: "ech outer name differing from the host", host: "secret.example.org", payload: withExtension(t, hello, extensionECH, echOuterPayload), wantErr: ErrECHOuterMismatch, mismatch: reasonECH},
		{name: "plain bytes pass through", host: "example.com", payload: []byte("GET / HTTP/1.1\r\n\r\n")},
		{name: "garbage after handshake byte", host: "example.com", payload: []byte{0x16, 0x03, 0x01, 0x00, 0x02, 0xff, 0xff}, wantErr: ErrMalformedHello},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, got, err := feed(t, tt.host, tt.payload)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if !bytes.Equal(got, tt.payload) {
					t.Fatalf("replayed %d bytes, want %d", len(got), len(tt.payload))
				}
			}
			if c.mismatch() != tt.mismatch {
				t.Fatalf("mismatch = %q, want %q", c.mismatch(), tt.mismatch)
			}
		})
	}
}

var echOuterPayload = []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x2a, 0x00, 0x04, 1, 2, 3, 4, 0x00, 0x04, 5, 6, 7, 8}

func withExtension(t *testing.T, record []byte, extType uint16, data []byte) []byte {
	t.Helper()
	out := bytes.Clone(record)
	pos := 5 + 4 + 2 + 32
	pos += 1 + int(out[pos])
	pos += 2 + (int(out[pos])<<8 | int(out[pos+1]))
	pos += 1 + int(out[pos])
	extLen := int(out[pos])<<8 | int(out[pos+1])
	if pos+2+extLen != len(out) {
		t.Fatalf("unexpected client hello layout")
	}
	ext := []byte{byte(extType >> 8), byte(extType), byte(len(data) >> 8), byte(len(data))}
	ext = append(ext, data...)
	out = append(out, ext...)
	grow := func(at, width, by int) {
		n := 0
		for i := range width {
			n = n<<8 | int(out[at+i])
		}
		n += by
		for i := width - 1; i >= 0; i-- {
			out[at+i] = byte(n)
			n >>= 8
		}
	}
	grow(pos, 2, len(ext))
	grow(6, 3, len(ext))
	grow(3, 2, len(ext))
	return out
}
