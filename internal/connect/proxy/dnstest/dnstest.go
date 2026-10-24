package dnstest

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
)

const (
	typeA       = 1
	typeAAAA    = 28
	classIN     = 1
	rcodeNX     = 3
	headerLen   = 12
	maxDatagram = 512
)

type Server struct {
	conn    net.PacketConn
	records map[string][]net.IP
	mu      sync.Mutex
	queries map[string]int
	done    chan struct{}
}

func Start(t testing.TB, records map[string][]string) *Server {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{conn: conn, records: map[string][]net.IP{}, queries: map[string]int{}, done: make(chan struct{})}
	for name, ips := range records {
		parsed := make([]net.IP, 0, len(ips))
		for _, ip := range ips {
			p := net.ParseIP(ip)
			if p == nil {
				t.Fatalf("dnstest: invalid ip %q", ip)
			}
			parsed = append(parsed, p)
		}
		s.records[canonical(name)] = parsed
	}
	go s.serve()
	t.Cleanup(func() {
		_ = conn.Close()
		<-s.done
	})
	return s
}

func (s *Server) Addr() string {
	return s.conn.LocalAddr().String()
}

func (s *Server) Resolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", s.Addr())
		},
	}
}

func (s *Server) Queries(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[canonical(name)]
}

func canonical(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func (s *Server) serve() {
	defer close(s.done)
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := s.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		reply, err := s.answer(buf[:n])
		if err != nil {
			continue
		}
		_, _ = s.conn.WriteTo(reply, from)
	}
}

func (s *Server) answer(query []byte) ([]byte, error) {
	if len(query) < headerLen || binary.BigEndian.Uint16(query[4:6]) != 1 {
		return nil, errors.New("dnstest: expected one question")
	}
	name, end, err := readName(query, headerLen)
	if err != nil || end+4 > len(query) {
		return nil, errors.New("dnstest: malformed question")
	}
	qtype := binary.BigEndian.Uint16(query[end : end+2])
	question := query[headerLen : end+4]

	s.mu.Lock()
	s.queries[name]++
	s.mu.Unlock()

	ips, known := s.records[name]
	var answers [][]byte
	for _, ip := range ips {
		v4 := ip.To4()
		switch {
		case qtype == typeA && v4 != nil:
			answers = append(answers, v4)
		case qtype == typeAAAA && v4 == nil:
			answers = append(answers, ip.To16())
		}
	}

	flags := uint16(0x8180)
	if !known {
		flags |= rcodeNX
	}
	reply := make([]byte, headerLen, headerLen+len(question)+len(answers)*28)
	copy(reply[0:2], query[0:2])
	binary.BigEndian.PutUint16(reply[2:4], flags)
	binary.BigEndian.PutUint16(reply[4:6], 1)
	binary.BigEndian.PutUint16(reply[6:8], uint16(len(answers)))
	reply = append(reply, question...)
	for _, rdata := range answers {
		rr := make([]byte, 12, 12+len(rdata))
		binary.BigEndian.PutUint16(rr[0:2], 0xC000|headerLen)
		binary.BigEndian.PutUint16(rr[2:4], qtype)
		binary.BigEndian.PutUint16(rr[4:6], classIN)
		binary.BigEndian.PutUint16(rr[10:12], uint16(len(rdata)))
		reply = append(reply, append(rr, rdata...)...)
	}
	return reply, nil
}

func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	for {
		if off >= len(msg) {
			return "", 0, errors.New("dnstest: name overruns message")
		}
		size := int(msg[off])
		off++
		if size == 0 {
			return canonical(strings.Join(labels, ".")), off, nil
		}
		if size > 63 || off+size > len(msg) {
			return "", 0, errors.New("dnstest: bad label")
		}
		labels = append(labels, string(msg[off:off+size]))
		off += size
	}
}
