package main

import (
	"flag"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type mapFlag []string

func (m *mapFlag) String() string     { return strings.Join(*m, ";") }
func (m *mapFlag) Set(v string) error { *m = append(*m, v); return nil }

type balancer struct {
	backends []string
	next     atomic.Uint64
	mu       sync.Mutex
	downTill map[string]time.Time
}

func (b *balancer) isDown(addr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().Before(b.downTill[addr])
}

func (b *balancer) markDown(addr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.downTill[addr] = time.Now().Add(time.Second)
}

func (b *balancer) dial() (net.Conn, error) {
	start := b.next.Add(1)
	var lastErr error
	for pass := 0; pass < 2; pass++ {
		for i := 0; i < len(b.backends); i++ {
			addr := b.backends[(int(start)+i)%len(b.backends)]
			if pass == 0 && b.isDown(addr) {
				continue
			}
			c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
			if err == nil {
				return c, nil
			}
			b.markDown(addr)
			lastErr = err
		}
	}
	return nil, lastErr
}

func serveLB(listen string, b *balancer) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			up, err := b.dial()
			if err != nil {
				return
			}
			defer up.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
			<-done
		}()
	}
}

func runLB(args []string) error {
	fs := flag.NewFlagSet("lb", flag.ExitOnError)
	var maps mapFlag
	fs.Var(&maps, "map", "listen=backend1,backend2 (repeatable)")
	_ = fs.Parse(args)

	errc := make(chan error, len(maps))
	for _, m := range maps {
		listen, rest, ok := strings.Cut(m, "=")
		if !ok {
			continue
		}
		b := &balancer{backends: strings.Split(rest, ","), downTill: map[string]time.Time{}}
		go func() { errc <- serveLB(listen, b) }()
	}
	return <-errc
}
