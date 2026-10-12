package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func loopbackGuardT(t *testing.T) *Guard {
	t.Helper()
	g, err := NewGuard(true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func certPEM(raw []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
}

func TestGuardDialTLSModes(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	target := "https://" + u.Host
	cert := srv.Certificate()
	caPEM := certPEM(cert.Raw)
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	goodPin := base64.StdEncoding.EncodeToString(sum[:])
	badSum := sha256.Sum256([]byte("other"))
	badPin := base64.StdEncoding.EncodeToString(badSum[:])

	tests := []struct {
		name    string
		otls    *store.OriginTLS
		wantErr bool
	}{
		{"system roots reject self-signed", nil, true},
		{"ca pem accepted via ip san", &store.OriginTLS{CAPEM: caPEM}, false},
		{"ca pem with matching server name", &store.OriginTLS{CAPEM: caPEM, ServerName: "example.com"}, false},
		{"ca pem with wrong server name", &store.OriginTLS{CAPEM: caPEM, ServerName: "other.test"}, true},
		{"pin match skips chain", &store.OriginTLS{PinSHA256: []string{goodPin}}, false},
		{"pin match among several", &store.OriginTLS{PinSHA256: []string{badPin, goodPin}}, false},
		{"pin mismatch", &store.OriginTLS{PinSHA256: []string{badPin}}, true},
		{"pin mismatch ignores good ca", &store.OriginTLS{PinSHA256: []string{badPin}, CAPEM: caPEM}, true},
		{"insecure skips everything", &store.OriginTLS{InsecureSkipVerify: true}, false},
		{"insecure beats bad pin", &store.OriginTLS{InsecureSkipVerify: true, PinSHA256: []string{badPin}}, false},
		{"invalid pin rejected", &store.OriginTLS{PinSHA256: []string{"nope"}}, true},
	}
	g := loopbackGuardT(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := g.Dial(context.Background(), target, tc.otls, 3*time.Second)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if c != nil {
				c.Close()
			}
		})
	}
}

func TestGuardDialPlainAndBlocked(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()
	c, err := loopbackGuardT(t).Dial(context.Background(), "http://"+ln.Addr().String(), nil, time.Second)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	c.Close()
	if _, err := DefaultGuard().Dial(context.Background(), "https://"+ln.Addr().String(), nil, time.Second); err == nil {
		t.Fatal("blocked loopback must not dial")
	}
}

func TestGuardDialHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(2 * time.Second)
			c.Close()
		}
	}()
	start := time.Now()
	_, err = loopbackGuardT(t).Dial(context.Background(), "https://"+ln.Addr().String(), nil, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected handshake timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("timeout not honoured: %v", time.Since(start))
	}
}

func TestValidateOriginTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	good := certPEM(srv.Certificate().Raw)
	pin := base64.StdEncoding.EncodeToString(make([]byte, 32))
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	tests := []struct {
		name    string
		in      *store.OriginTLS
		wantErr bool
	}{
		{"nil", nil, false},
		{"empty", &store.OriginTLS{}, false},
		{"good ca", &store.OriginTLS{CAPEM: good}, false},
		{"bad ca", &store.OriginTLS{CAPEM: "garbage"}, true},
		{"good pin", &store.OriginTLS{PinSHA256: []string{pin}}, false},
		{"short pin", &store.OriginTLS{PinSHA256: []string{short}}, true},
		{"non base64 pin", &store.OriginTLS{PinSHA256: []string{"%%%"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateOriginTLS(tc.in); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
