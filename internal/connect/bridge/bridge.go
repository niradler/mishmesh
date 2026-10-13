package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type Meter interface {
	AddBytes(kind string, in, out int64)
}

type Options struct {
	Data  store.DataStore
	Conns store.ConnectionStore
	Log   *slog.Logger
	Meter Meter
}

type Bridge struct {
	data  store.DataStore
	conns store.ConnectionStore
	log   *slog.Logger
	meter Meter
}

const lookupTimeout = 5 * time.Second

var (
	ErrKindMismatch   = errors.New("connect: endpoint kind does not match the stream")
	ErrNotAllowed     = errors.New("connect: agent is not allowed to connect to this endpoint")
	ErrOriginOffline  = errors.New("connect: endpoint has no connected origin")
	ErrMissingConnect = errors.New("connect: stream does not name an endpoint")
)

func New(opts Options) *Bridge {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Bridge{data: opts.Data, conns: opts.Conns, log: log, meter: opts.Meter}
}

func (b *Bridge) HandleConnect(ctx context.Context, agent *store.Agent, stream net.Conn, init tunnel.StreamInit) {
	defer stream.Close()
	ep, origin, err := b.authorize(ctx, agent, init)
	if err != nil {
		b.log.Warn("connect refused", "agent_id", agent.ID, "connect", init.Meta[tunnel.MetaConnect], "kind", init.Kind, "err", err)
		return
	}
	meta := map[string]string{}
	if v := init.Meta[tunnel.MetaIdleTimeout]; v != "" {
		meta[tunnel.MetaIdleTimeout] = v
	}
	upstream, err := origin.OpenStream(ctx, ep.ID, ep.Kind, meta)
	if err != nil {
		b.log.Warn("connect origin stream failed", "agent_id", agent.ID, "endpoint_id", ep.ID, "err", err)
		return
	}
	defer upstream.Close()
	b.log.Debug("connect bridged", "agent_id", agent.ID, "endpoint_id", ep.ID, "kind", ep.Kind)
	up, down := tunnel.Splice(stream, upstream)
	b.conns.AddUsage(ep.OrgID, up+down)
	if b.meter != nil {
		b.meter.AddBytes(ep.Kind, up, down)
	}
}

func (b *Bridge) authorize(ctx context.Context, agent *store.Agent, init tunnel.StreamInit) (*store.Endpoint, store.AgentConn, error) {
	ref := init.Meta[tunnel.MetaConnect]
	if ref == "" {
		return nil, nil, ErrMissingConnect
	}
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	ep, err := store.FindConnectEndpoint(lookupCtx, b.data, agent.OrgID, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("connect: endpoint %q: %w", ref, err)
	}
	if ep.Kind != init.Kind {
		return nil, nil, fmt.Errorf("%w: endpoint is %s, stream is %s", ErrKindMismatch, ep.Kind, init.Kind)
	}
	if ep.Policy == nil || !ep.Policy.Connect.Allows(agent) {
		return nil, nil, ErrNotAllowed
	}
	origin, ok := b.conns.ResolveEndpoint(ep.ID)
	if !ok {
		return nil, nil, ErrOriginOffline
	}
	return ep, origin, nil
}
