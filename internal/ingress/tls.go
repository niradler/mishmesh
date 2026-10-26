package ingress

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type TLSPassthroughOptions struct {
	Data       store.DataStore
	Conns      store.ConnectionStore
	Log        *slog.Logger
	BaseDomain string
	Meter      Meter
}

type TLSPassthrough struct {
	data     store.DataStore
	conns    store.ConnectionStore
	log      *slog.Logger
	apexHost string
	meter    Meter
	ln       net.Listener

	helloTimeout time.Duration
}

const (
	defaultClientHelloTimeout = 10 * time.Second
	maxClientHelloRecord      = 1 << 14
)

func NewTLSPassthrough(opts TLSPassthroughOptions) *TLSPassthrough {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &TLSPassthrough{
		data:     opts.Data,
		conns:    opts.Conns,
		log:      log,
		apexHost: hostOnly(opts.BaseDomain),
		meter:    opts.Meter,

		helloTimeout: defaultClientHelloTimeout,
	}
}

func (t *TLSPassthrough) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("tls passthrough listen %s: %w", addr, err)
	}
	t.ln = ln
	go t.serve()
	return nil
}

func (t *TLSPassthrough) Shutdown() {
	if t.ln != nil {
		_ = t.ln.Close()
	}
}

func (t *TLSPassthrough) serve() {
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			return
		}
		go t.handle(conn)
	}
}

func (t *TLSPassthrough) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(t.helloTimeout))
	sni, hello, err := readClientHelloSNI(client)
	_ = client.SetReadDeadline(time.Time{})
	if err != nil {
		t.log.Warn("tls passthrough: read client hello failed", "err", err)
		return
	}
	ep := t.resolveSNI(sni)
	if ep == nil {
		t.log.Warn("tls passthrough: no endpoint for sni", "sni", sni)
		return
	}
	agent, ok := t.conns.ResolveEndpoint(ep.ID)
	if !ok {
		return
	}
	openCtx, cancelOpen := context.WithTimeout(context.Background(), streamOpenTimeout)
	stream, err := agent.OpenStream(openCtx, ep.ID, store.KindTLS, withClientIP(nil, peerIP(client.RemoteAddr())))
	cancelOpen()
	if err != nil {
		t.log.Warn("tls passthrough: open stream failed", "endpoint_id", ep.ID, "err", err)
		return
	}
	metered := meterStream(stream, t.conns, t.meter, ep.OrgID, store.KindTLS)
	defer metered.Close()

	if _, err := metered.Write(hello); err != nil {
		return
	}
	tunnel.Splice(client, metered)
}

func (t *TLSPassthrough) resolveSNI(sni string) *store.Endpoint {
	host := strings.ToLower(strings.TrimSpace(sni))
	if host == "" {
		return nil
	}
	suffix := "." + t.apexHost
	if label, ok := strings.CutSuffix(host, suffix); ok {
		if label != "" && !strings.Contains(label, ".") {
			if ep, err := t.data.GetEndpointBySubdomain(context.Background(), label); err == nil && ep.Kind == store.KindTLS {
				return ep
			}
		}
	}
	if ep, err := t.data.GetEndpointByDomain(context.Background(), host); err == nil && ep.Kind == store.KindTLS {
		return ep
	}
	return nil
}

func readClientHelloSNI(r io.Reader) (sni string, raw []byte, err error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", nil, err
	}
	if header[0] != 0x16 {
		return "", nil, fmt.Errorf("tls passthrough: not a handshake record")
	}
	recLen := int(binary.BigEndian.Uint16(header[3:5]))
	if recLen <= 0 || recLen > maxClientHelloRecord {
		return "", nil, fmt.Errorf("tls passthrough: bad record length %d", recLen)
	}
	raw = make([]byte, len(header)+recLen)
	copy(raw, header)
	if _, err := io.ReadFull(r, raw[len(header):]); err != nil {
		return "", nil, err
	}
	sni = parseSNI(raw[len(header):])
	return sni, raw, nil
}

func parseSNI(b []byte) string {
	if len(b) < 4 || b[0] != 0x01 {
		return ""
	}
	p := b[4:]
	if len(p) < 34 {
		return ""
	}
	p = p[34:]
	if len(p) < 1 {
		return ""
	}
	sidLen := int(p[0])
	p = p[1:]
	if len(p) < sidLen+2 {
		return ""
	}
	p = p[sidLen:]
	csLen := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if len(p) < csLen+1 {
		return ""
	}
	p = p[csLen:]
	compLen := int(p[0])
	p = p[1:]
	if len(p) < compLen+2 {
		return ""
	}
	p = p[compLen:]
	extTotal := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if len(p) < extTotal {
		return ""
	}
	p = p[:extTotal]
	for len(p) >= 4 {
		extType := binary.BigEndian.Uint16(p[:2])
		extLen := int(binary.BigEndian.Uint16(p[2:4]))
		p = p[4:]
		if len(p) < extLen {
			return ""
		}
		if extType == 0x0000 {
			return parseServerNameExtension(p[:extLen])
		}
		p = p[extLen:]
	}
	return ""
}

func parseServerNameExtension(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(b[:2]))
	b = b[2:]
	if len(b) < listLen {
		return ""
	}
	for len(b) >= 3 {
		nameType := b[0]
		nameLen := int(binary.BigEndian.Uint16(b[1:3]))
		b = b[3:]
		if len(b) < nameLen {
			return ""
		}
		if nameType == 0x00 {
			return string(b[:nameLen])
		}
		b = b[nameLen:]
	}
	return ""
}
