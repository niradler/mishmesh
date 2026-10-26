package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type ConnectSpec struct {
	Name     string
	Endpoint string
	Kind     string
	Listen   string
	Idle     time.Duration
}

var errNotConnected = errors.New("tunnel is not connected to the gateway")

func (a *Agent) startConnects(ctx context.Context) (func(), error) {
	var closers []func()
	var wg sync.WaitGroup
	var once sync.Once
	stop := func() {
		once.Do(func() {
			for _, c := range closers {
				c()
			}
			wg.Wait()
		})
	}
	for _, spec := range a.opts.Connects {
		closeFn, err := a.listenConnect(ctx, spec, &wg)
		if err != nil {
			stop()
			return nil, fmt.Errorf("connect %q: %w", spec.Name, err)
		}
		closers = append(closers, closeFn)
	}
	if a.opts.Egress != nil {
		closeFn, err := a.listenEgress(ctx, *a.opts.Egress, &wg)
		if err != nil {
			stop()
			return nil, fmt.Errorf("egress: %w", err)
		}
		closers = append(closers, closeFn)
	}
	if len(closers) > 0 {
		go func() {
			<-ctx.Done()
			stop()
		}()
	}
	return stop, nil
}

func (a *Agent) listenConnect(ctx context.Context, spec ConnectSpec, wg *sync.WaitGroup) (func(), error) {
	switch spec.Kind {
	case store.KindUDP:
		pc, err := net.ListenPacket("udp", spec.Listen)
		if err != nil {
			return nil, err
		}
		dl := tunnel.NewDatagramListener(pc, tunnel.DatagramListenerOptions{
			Open: func(net.Addr) (net.Conn, error) { return a.openConnect(ctx, spec) },
			Idle: spec.Idle,
		})
		wg.Go(func() {
			if err := dl.Serve(); err != nil {
				a.log.Warn("connect listener stopped", "connect", spec.Name, "err", err)
			}
		})
		a.log.Info("connect listening", "connect", spec.Name, "endpoint", spec.Endpoint, "proto", spec.Kind, "addr", pc.LocalAddr().String())
		return func() { _ = pc.Close() }, nil
	case store.KindTCP:
		ln, err := net.Listen("tcp", spec.Listen)
		if err != nil {
			return nil, err
		}
		wg.Go(func() { a.acceptConnect(ctx, ln, spec) })
		a.log.Info("connect listening", "connect", spec.Name, "endpoint", spec.Endpoint, "proto", spec.Kind, "addr", ln.Addr().String())
		return func() { _ = ln.Close() }, nil
	default:
		return nil, fmt.Errorf("unsupported connect proto %q (want tcp or udp)", spec.Kind)
	}
}

func (a *Agent) acceptConnect(ctx context.Context, ln net.Listener, spec ConnectSpec) {
	for {
		client, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer client.Close()
			stream, err := a.openConnect(ctx, spec)
			if err != nil {
				a.log.Warn("connect stream failed", "connect", spec.Name, "err", err)
				return
			}
			defer stream.Close()
			tunnel.Splice(client, stream)
		}()
	}
}

func (a *Agent) openConnect(ctx context.Context, spec ConnectSpec) (net.Conn, error) {
	if err := a.dec.check(ctx, spec.Endpoint, spec.Kind); err != nil {
		return nil, err
	}
	sess := a.sess.Load()
	if sess == nil {
		return nil, errNotConnected
	}
	meta := map[string]string{tunnel.MetaConnect: spec.Endpoint}
	if spec.Kind == store.KindUDP {
		meta = tunnel.WithDatagramIdle(meta, spec.Idle)
	}
	return sess.OpenData(tunnel.StreamInit{Kind: spec.Kind, Meta: meta})
}

func (a *Agent) relayUDP(ctx context.Context, stream net.Conn, init tunnel.StreamInit, tgt localTarget) {
	ctx, cancel := context.WithTimeout(ctx, localDialTimeout)
	defer cancel()
	var dialer net.Dialer
	socket, err := dialer.DialContext(ctx, "udp", tgt.addr)
	if err != nil {
		a.log.Warn("dial local udp target failed", "target", tgt.addr, "err", err)
		return
	}
	tunnel.RelayDatagrams(stream, socket, tunnel.DatagramIdleFromMeta(init.Meta))
}
