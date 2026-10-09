package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) { return *(v.(*[]byte)), nil }

func (rawCodec) Unmarshal(data []byte, v any) error {
	p := v.(*[]byte)
	*p = append((*p)[:0], data...)
	return nil
}

func (rawCodec) Name() string { return "raw" }

var echoDesc = grpc.ServiceDesc{
	ServiceName: "kit.Echo",
	HandlerType: (*any)(nil),
	Methods:     []grpc.MethodDesc{{MethodName: "Unary", Handler: unaryHandler}},
	Streams:     []grpc.StreamDesc{{StreamName: "Bidi", Handler: bidiHandler, ServerStreams: true, ClientStreams: true}},
}

func unaryHandler(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	var in []byte
	if err := dec(&in); err != nil {
		return nil, err
	}
	out := append([]byte("echo:"), in...)
	return &out, nil
}

func bidiHandler(_ any, stream grpc.ServerStream) error {
	for {
		var m []byte
		if err := stream.RecvMsg(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := stream.SendMsg(&m); err != nil {
			return err
		}
	}
}

func newGRPCServer() *grpc.Server {
	s := grpc.NewServer(grpc.ForceServerCodec(rawCodec{}), grpc.MaxRecvMsgSize(64<<20), grpc.MaxSendMsgSize(64<<20))
	s.RegisterService(&echoDesc, struct{}{})
	return s
}

func cmdGRPC(args []string) error {
	fs := flag.NewFlagSet("grpc", flag.ContinueOnError)
	addr := fs.String("addr", "", "host:port")
	useTLS := fs.Bool("tls", false, "dial with TLS (insecure skip verify)")
	authority := fs.String("authority", "", "override :authority / SNI")
	msgs := fs.Int("n", 100, "bidi messages")
	size := fs.String("size", "64K", "bidi message size")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	msgSize, err := parseSize(*size)
	if err != nil {
		return err
	}
	opts := []grpc.DialOption{grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{}), grpc.MaxCallRecvMsgSize(64<<20), grpc.MaxCallSendMsgSize(64<<20))}
	if *useTLS {
		cfg := &tls.Config{InsecureSkipVerify: true, ServerName: *authority}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	if *authority != "" {
		opts = append(opts, grpc.WithAuthority(*authority))
	}
	conn, err := grpc.NewClient("passthrough:///"+*addr, opts...)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	in := []byte("hello")
	var out []byte
	if err := conn.Invoke(ctx, "/kit.Echo/Unary", &in, &out); err != nil {
		return fmt.Errorf("unary: %w", err)
	}
	if string(out) != "echo:hello" {
		return fmt.Errorf("unary: unexpected reply %q", out)
	}
	fmt.Println("unary ok")

	stream, err := conn.NewStream(ctx, &echoDesc.Streams[0], "/kit.Echo/Bidi")
	if err != nil {
		return fmt.Errorf("bidi open: %w", err)
	}
	start := time.Now()
	errc := make(chan error, 1)
	go func() {
		for i := range *msgs {
			m := bytes.Repeat([]byte{byte(i)}, int(msgSize))
			if err := stream.SendMsg(&m); err != nil {
				errc <- fmt.Errorf("bidi send %d: %w", i, err)
				return
			}
		}
		errc <- stream.CloseSend()
	}()
	for i := range *msgs {
		var m []byte
		if err := stream.RecvMsg(&m); err != nil {
			return fmt.Errorf("bidi recv %d: %w", i, err)
		}
		if len(m) != int(msgSize) || m[0] != byte(i) || m[len(m)-1] != byte(i) {
			return fmt.Errorf("bidi recv %d: corrupt message len=%d", i, len(m))
		}
	}
	if err := <-errc; err != nil {
		return err
	}
	var tail []byte
	if err := stream.RecvMsg(&tail); !errors.Is(err, io.EOF) {
		return fmt.Errorf("bidi end: expected EOF, got %v", err)
	}
	fmt.Printf("bidi ok msgs=%d size=%d elapsed=%s\n", *msgs, msgSize, time.Since(start).Round(time.Millisecond))
	return nil
}
