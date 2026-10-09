package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc"
)

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	httpAddr := fs.String("http", ":8080", "http/1.1 + h2c listener")
	tlsAddr := fs.String("tls", ":8443", "https listener with its own cert")
	rawAddr := fs.String("raw", ":9000", "half-close tcp listener")
	grpcAddr := fs.String("grpc", ":9090", "grpc h2c listener")
	grpcTLSAddr := fs.String("grpctls", ":9443", "grpc tls listener")
	cn := fs.String("cn", "kit-backend", "backend certificate common name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cert, err := selfSigned(*cn)
	if err != nil {
		return err
	}
	gs := newGRPCServer()
	mux := backendMux(gs)

	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetUnencryptedHTTP2(true)
	plain := &http.Server{Addr: *httpAddr, Handler: mux, Protocols: &protos, ReadHeaderTimeout: 10 * time.Second}
	secure := &http.Server{Addr: *tlsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}}

	errc := make(chan error, 5)
	go func() { errc <- plain.ListenAndServe() }()
	go func() { errc <- secure.ListenAndServeTLS("", "") }()
	go func() { errc <- serveRaw(*rawAddr) }()
	go func() {
		ln, err := net.Listen("tcp", *grpcAddr)
		if err != nil {
			errc <- err
			return
		}
		errc <- gs.Serve(ln)
	}()
	go func() {
		ln, err := tls.Listen("tcp", *grpcTLSAddr, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}})
		if err != nil {
			errc <- err
			return
		}
		errc <- newGRPCServer().Serve(ln)
	}()
	log.Printf("kit backend up http=%s tls=%s raw=%s grpc=%s grpctls=%s", *httpAddr, *tlsAddr, *rawAddr, *grpcAddr, *grpcTLSAddr)
	return <-errc
}

func backendMux(gs *grpc.Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "kit ok proto=%s host=%s path=%s tls=%v\n", r.Proto, r.Host, r.URL.Path, r.TLS != nil)
	})
	mux.HandleFunc("/gen", handleGen)
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/sse", handleSSE)
	mux.HandleFunc("/slow", handleSlow)
	mux.HandleFunc("/ws", handleWS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gs.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func handleGen(w http.ResponseWriter, r *http.Request) {
	size, err := parseSize(r.URL.Query().Get("size"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	seed, _ := strconv.ParseUint(r.URL.Query().Get("seed"), 10, 64)
	if seed == 0 {
		seed = 1
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.URL.Query().Get("chunked") == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.CopyBuffer(w, newGen(size, seed), make([]byte, 64<<10))
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	h := sha256.New()
	start := time.Now()
	n, err := io.CopyBuffer(h, r.Body, make([]byte, 64<<10))
	resp := map[string]any{
		"bytes":          n,
		"sha256":         hex.EncodeToString(h.Sum(nil)),
		"content_length": r.ContentLength,
		"te":             r.TransferEncoding,
		"expect":         r.Header.Get("Expect"),
		"seconds":        time.Since(start).Seconds(),
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleSSE(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 {
		n = 10
	}
	every, err := time.ParseDuration(r.URL.Query().Get("every"))
	if err != nil {
		every = time.Second
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	t := time.NewTicker(every)
	defer t.Stop()
	for i := 1; i <= n; i++ {
		fmt.Fprintf(w, "id: %d\ndata: tick %d %s\n\n", i, i, time.Now().UTC().Format(time.RFC3339))
		fl.Flush()
		if i == n {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func handleSlow(w http.ResponseWriter, r *http.Request) {
	d, err := time.ParseDuration(r.URL.Query().Get("d"))
	if err != nil {
		d = 3 * time.Minute
	}
	select {
	case <-r.Context().Done():
		return
	case <-time.After(d):
	}
	fmt.Fprintf(w, "slow done after %s\n", d)
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(256 << 20)
	ctx := context.Background()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if err := c.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

func serveRaw(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			h := sha256.New()
			n, err := io.Copy(h, c)
			if err != nil {
				fmt.Fprintf(c, "read error after %d bytes: %v\n", n, err)
				return
			}
			fmt.Fprintf(c, "got %d bytes sha256=%s\n", n, hex.EncodeToString(h.Sum(nil)))
		}(c)
	}
}

func selfSigned(cn string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"kit backend"}},
		DNSNames:     []string{cn, "kit", "*.mm.test", "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
