package agent

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDialTargetHTTP2ALPN(t *testing.T) {
	for _, supported := range []bool{true, false} {
		name := "supported"
		if !supported {
			name = "unsupported"
		}
		t.Run(name, func(t *testing.T) {
			backend := httptest.NewUnstartedServer(http.NotFoundHandler())
			backend.EnableHTTP2 = supported
			backend.StartTLS()
			t.Cleanup(backend.Close)
			connection, err := dialTarget(localTarget{addr: backend.Listener.Addr().String(), useTLS: true, insecure: true}, "h2")
			if !supported {
				if err == nil {
					connection.Close()
					t.Fatal("HTTP/1-only TLS target must reject requested HTTP/2")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if got := connection.(*tls.Conn).ConnectionState().NegotiatedProtocol; got != "h2" {
				t.Fatalf("ALPN = %q", got)
			}
		})
	}
}
