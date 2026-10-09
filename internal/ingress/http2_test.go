package ingress

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestGRPCUpstreamProtocolAndEndpointIsolation(t *testing.T) {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	backends := make([]*httptest.Server, 2)
	for index := range backends {
		backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Trailer", "Grpc-Status")
			fmt.Fprintf(w, "%d:%s", index, r.Proto)
			w.Header().Set("Grpc-Status", "0")
		}))
		backend.Config.Protocols = &protocols
		backend.Start()
		t.Cleanup(backend.Close)
		backends[index] = backend
	}
	ingress := &Ingress{log: discardLogger(), conns: nopConns{}}
	ingress.proxy = ingress.newProxy(time.Second)
	for _, mediaType := range []string{"application/grpc", "application/grpc+proto", "text/plain", "application/grpc-web+proto"} {
		for index, backend := range backends {
			t.Run(fmt.Sprintf("%s/%d", mediaType, index), func(t *testing.T) {
				agent := &scriptedAgent{open: func() (net.Conn, error) {
					return net.DialTimeout("tcp", backend.Listener.Addr().String(), time.Second)
				}}
				request := httptest.NewRequest("POST", "http://demo.example.com/echo", strings.NewReader("hello"))
				request.Header.Set("Content-Type", mediaType)
				response := httptest.NewRecorder()
				ingress.proxyHTTP(response, request, agent, &store.Endpoint{ID: fmt.Sprintf("ep_%d", index), Kind: store.KindHTTP}, "/echo", 0)
				wantProtocol := "HTTP/1.1"
				if mediaType == "application/grpc" || mediaType == "application/grpc+proto" {
					wantProtocol = "HTTP/2.0"
				}
				result := response.Result()
				defer result.Body.Close()
				body, err := io.ReadAll(result.Body)
				if err != nil || result.StatusCode != 200 || string(body) != fmt.Sprintf("%d:%s", index, wantProtocol) || result.Trailer.Get("Grpc-Status") != "0" {
					t.Fatalf("code=%d body=%q trailer=%v err=%v", result.StatusCode, body, result.Trailer, err)
				}
			})
		}
	}
}

func TestGRPCUpstreamHeaderTimeout(t *testing.T) {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	backend.Config.Protocols = &protocols
	backend.Start()
	t.Cleanup(backend.Close)
	ingress := &Ingress{log: discardLogger(), conns: nopConns{}}
	ingress.proxy = ingress.newProxy(50 * time.Millisecond)
	agent := &scriptedAgent{open: func() (net.Conn, error) {
		return net.DialTimeout("tcp", backend.Listener.Addr().String(), time.Second)
	}}
	request := httptest.NewRequest("POST", "http://demo.example.com/echo", strings.NewReader("hello"))
	request.Header.Set("Content-Type", "application/grpc")
	response := httptest.NewRecorder()
	ingress.proxyHTTP(response, request, agent, &store.Endpoint{ID: "ep_test", Kind: store.KindHTTP}, "/echo", 0)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
