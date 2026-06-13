package ingress

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestReadClientHelloSNI(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		c := tls.Client(client, &tls.Config{ServerName: "demo.example.com", InsecureSkipVerify: true})
		_ = c.HandshakeContext(context.Background())
	}()

	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	sni, raw, err := readClientHelloSNI(server)
	if err != nil {
		t.Fatalf("readClientHelloSNI: %v", err)
	}
	if sni != "demo.example.com" {
		t.Fatalf("sni = %q want demo.example.com", sni)
	}
	if len(raw) < 5 || raw[0] != 0x16 {
		t.Fatalf("raw record malformed: %v", raw[:min(5, len(raw))])
	}
}

func TestParseSNIEmptyOnGarbage(t *testing.T) {
	if got := parseSNI([]byte{0x02, 0x00, 0x00, 0x00}); got != "" {
		t.Fatalf("expected empty sni, got %q", got)
	}
}
