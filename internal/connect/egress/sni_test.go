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
		mismatch bool
	}{
		{name: "matching sni replays hello", host: "api.example.com", payload: hello},
		{name: "matching sni ignores case and trailing dot", host: "api.example.com", payload: clientHelloBytes(t, "API.Example.com.")},
		{name: "no sni passes", host: "93.184.216.34", payload: clientHelloBytes(t, "")},
		{name: "mismatched sni", host: "93.184.216.34", payload: hello, wantErr: ErrSNIMismatch, mismatch: true},
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
				t.Fatalf("mismatch = %v, want %v", c.mismatch(), tt.mismatch)
			}
		})
	}
}
