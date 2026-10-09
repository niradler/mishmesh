package ingress

import (
	"bytes"
	"context"
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

func TestGRPCBidirectionalHTTP1ProxyHop(t *testing.T) {
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "HTTP/2 required", http.StatusHTTPVersionNotSupported)
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		for range 3 {
			message := make([]byte, 64<<10)
			if _, err := io.ReadFull(r.Body, message); err != nil {
				return
			}
			if _, err := w.Write(message); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
		w.Header().Set("Grpc-Status", "0")
	}))
	backend.Config.Protocols = &protocols
	backend.Start()
	t.Cleanup(backend.Close)
	ingress := &Ingress{log: discardLogger(), conns: nopConns{}}
	ingress.proxy = ingress.newProxy(time.Second)
	agent := &scriptedAgent{open: func() (net.Conn, error) {
		return net.DialTimeout("tcp", backend.Listener.Addr().String(), time.Second)
	}}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingress.proxyHTTP(w, r, agent, &store.Endpoint{ID: "ep_duplex", Kind: store.KindHTTP}, "/echo", 0)
	}))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	stop := context.AfterFunc(ctx, func() {
		reader.CloseWithError(ctx.Err())
		writer.CloseWithError(ctx.Err())
	})
	defer stop()
	request, err := http.NewRequestWithContext(ctx, "POST", front.URL, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/grpc")
	type responseResult struct {
		response *http.Response
		err      error
	}
	responses := make(chan responseResult, 1)
	go func() {
		response, err := front.Client().Do(request)
		responses <- responseResult{response, err}
	}()
	first := bytes.Repeat([]byte{1}, 64<<10)
	if _, err := writer.Write(first); err != nil {
		t.Fatal(err)
	}
	result := <-responses
	if result.err != nil {
		t.Fatalf("response cannot start while request is open: %v", result.err)
	}
	defer result.response.Body.Close()
	if result.response.StatusCode != http.StatusOK {
		t.Fatalf("response status %d", result.response.StatusCode)
	}
	for index := range 3 {
		message := bytes.Repeat([]byte{byte(index + 1)}, 64<<10)
		if index > 0 {
			if _, err := writer.Write(message); err != nil {
				t.Fatal(err)
			}
		}
		echo := make([]byte, len(message))
		if _, err := io.ReadFull(result.response.Body, echo); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(echo, message) {
			t.Fatalf("bidirectional message %d corrupted", index)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, result.response.Body); err != nil {
		t.Fatal(err)
	}
	if result.response.Trailer.Get("Grpc-Status") != "0" {
		t.Fatalf("gRPC status trailer missing: %v", result.response.Trailer)
	}
}

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
				front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ingress.proxyHTTP(w, r, agent, &store.Endpoint{ID: fmt.Sprintf("ep_%d", index), Kind: store.KindHTTP}, "/echo", 0)
				}))
				defer front.Close()
				request, err := http.NewRequest("POST", front.URL, strings.NewReader("hello"))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", mediaType)
				wantProtocol := "HTTP/1.1"
				if mediaType == "application/grpc" || mediaType == "application/grpc+proto" {
					wantProtocol = "HTTP/2.0"
				}
				client := front.Client()
				client.Timeout = 3 * time.Second
				result, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
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
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingress.proxyHTTP(w, r, agent, &store.Endpoint{ID: "ep_test", Kind: store.KindHTTP}, "/echo", 0)
	}))
	defer front.Close()
	request, err := http.NewRequest("POST", front.URL, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/grpc")
	client := front.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
